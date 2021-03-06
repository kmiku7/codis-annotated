// Copyright 2014 Wandoujia Inc. All Rights Reserved.
// Licensed under the MIT (MIT-LICENSE.txt) license.

package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/wandoulabs/zkhelper"

	"github.com/wandoulabs/codis/pkg/models"
	"github.com/wandoulabs/codis/pkg/utils"
	"github.com/wandoulabs/codis/pkg/utils/errors"
	"github.com/wandoulabs/codis/pkg/utils/log"
)

type MigrateTaskInfo struct {
	SlotId     int    `json:"slot_id"`
	NewGroupId int    `json:"new_group"`
	// 按 key 迁移, 这个参数制定迁移每个key之间的延迟时间
	Delay      int    `json:"delay"`
	// 任务发起时间
	CreateAt   string `json:"create_at"`
	// 迁移百分比(?)
	Percent    int    `json:"percent"`
	Status     string `json:"status"`
	Id         string `json:"-"`
}

type SlotMigrateProgress struct {
	SlotId    int `json:"slot_id"`
	FromGroup int `json:"from"`
	ToGroup   int `json:"to"`
	Remain    int `json:"remain"`
}

func (p SlotMigrateProgress) String() string {
	return fmt.Sprintf("migrate Slot: slot_%d From: group_%d To: group_%d remain: %d keys", p.SlotId, p.FromGroup, p.ToGroup, p.Remain)
}

type MigrateTask struct {
	MigrateTaskInfo
	zkConn       zkhelper.Conn
	productName  string
	// 迁移进度信号
	progressChan chan SlotMigrateProgress
}

func GetMigrateTask(info MigrateTaskInfo) *MigrateTask {
	return &MigrateTask{
		MigrateTaskInfo: info,
		productName:     globalEnv.ProductName(),
		zkConn:          safeZkConn,
	}
}

// 这个Status是处理阶段, 不是进度(?)
func (t *MigrateTask) UpdateStatus(status string) {
	t.Status = status
	b, _ := json.Marshal(t.MigrateTaskInfo)
	t.zkConn.Set(getMigrateTasksPath(t.productName)+"/"+t.Id, b, -1)
}

func (t *MigrateTask) UpdateFinish() {
	// 都删除了还设置啥finish啊
	t.Status = MIGRATE_TASK_FINISHED
	t.zkConn.Delete(getMigrateTasksPath(t.productName)+"/"+t.Id, -1)
}
// 迁移 slot:slotId 到 group:to.
func (t *MigrateTask) migrateSingleSlot(slotId int, to int) error {
	// set slot status
	s, err := models.GetSlot(t.zkConn, t.productName, slotId)
	if err != nil {
		log.ErrorErrorf(err, "get slot info failed")
		return err
	}
	if s.State.Status == models.SLOT_STATUS_OFFLINE {
		log.Warnf("status is offline: %+v", s)
		return nil
	}

	// 如果是未迁移状态, s.GroupId是源group.
	// 如果是迁移状态, s.GroupId是目标group, 此时MigrateStatus保存的源和目标group
	from := s.GroupId
	if s.State.Status == models.SLOT_STATUS_MIGRATE {
		from = s.State.MigrateStatus.From
		// assert(s.State.MigrateStatus.To == to)
	}

	// make sure from group & target group exists
	exists, err := models.GroupExists(t.zkConn, t.productName, from)
	if err != nil {
		return errors.Trace(err)
	}
	if !exists {
		log.Errorf("src group %d not exist when migrate from %d to %d", from, from, to)
		return errors.Errorf("group %d not found", from)
	}

	exists, err = models.GroupExists(t.zkConn, t.productName, to)
	if err != nil {
		return errors.Trace(err)
	}
	if !exists {
		return errors.Errorf("group %d not found", to)
	}

	// cannot migrate to itself, just ignore
	if from == to {
		log.Warnf("from == to, ignore: %+v", s)
		return nil
	}

	// <del>modify slot status</del>
	// <del>设置zk信息</del>
	// 注意这个
	// 这里是阻塞的, 如果成功则表示迁移信息同步到各个proxy里了
	if err := s.SetMigrateStatus(t.zkConn, from, to); err != nil {
		log.ErrorErrorf(err, "set migrate status failed")
		return err
	}

	// 阻塞的
	err = t.Migrate(s, from, to, func(p SlotMigrateProgress) {
		// on migrate slot progress
		if p.Remain%5000 == 0 {
			log.Infof("%+v", p)
		}
	})
	if err != nil {
		log.ErrorErrorf(err, "migrate slot failed")
		return err
	}

	// migrate done, change slot status back
	s.State.Status = models.SLOT_STATUS_ONLINE
	s.State.MigrateStatus.From = models.INVALID_ID
	s.State.MigrateStatus.To = models.INVALID_ID
	if err := s.Update(t.zkConn); err != nil {
		log.ErrorErrorf(err, "update zk status failed, should be: %+v", s)
		return err
	}
	return nil
}

func (t *MigrateTask) run() error {
	// 看这两个log.Infof()就像阻塞的
	log.Infof("migration start: %+v", t.MigrateTaskInfo)
	to := t.NewGroupId
	t.UpdateStatus(MIGRATE_TASK_MIGRATING)
	err := t.migrateSingleSlot(t.SlotId, to)
	if err != nil {
		log.ErrorErrorf(err, "migrate single slot failed")
		t.UpdateStatus(MIGRATE_TASK_ERR)
		return err
	}
	t.UpdateFinish()
	log.Infof("migration finished: %+v", t.MigrateTaskInfo)
	return nil
}

var ErrGroupMasterNotFound = errors.New("group master not found")

// will block until all keys are migrated
func (task *MigrateTask) Migrate(slot *models.Slot, fromGroup, toGroup int, onProgress func(SlotMigrateProgress)) (err error) {
	groupFrom, err := models.GetGroup(task.zkConn, task.productName, fromGroup)
	if err != nil {
		return err
	}
	groupTo, err := models.GetGroup(task.zkConn, task.productName, toGroup)
	if err != nil {
		return err
	}

	fromMaster, err := groupFrom.Master(task.zkConn)
	if err != nil {
		return err
	}

	toMaster, err := groupTo.Master(task.zkConn)
	if err != nil {
		return err
	}

	if fromMaster == nil || toMaster == nil {
		return errors.Trace(ErrGroupMasterNotFound)
	}

	c, err := utils.DialTo(fromMaster.Addr, globalEnv.Password())
	if err != nil {
		return err
	}

	defer c.Close()

	// c 是 fromMaster 的连接
	_, remain, err := utils.SlotsMgrtTagSlot(c, slot.Id, toMaster.Addr)
	if err != nil {
		return err
	}

	for remain > 0 {
		if task.Delay > 0 {
			time.Sleep(time.Duration(task.Delay) * time.Millisecond)
		}
		_, remain, err = utils.SlotsMgrtTagSlot(c, slot.Id, toMaster.Addr)
		if remain >= 0 {
			onProgress(SlotMigrateProgress{
				SlotId:    slot.Id,
				FromGroup: fromGroup,
				ToGroup:   toGroup,
				Remain:    remain,
			})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// 约束条件检查
func (t *MigrateTask) preMigrateCheck() error {
	slots, err := models.GetMigratingSlots(safeZkConn, t.productName)

	if err != nil {
		return errors.Trace(err)
	}
	// check if there is migrating slot
	if len(slots) > 1 {
		return errors.Errorf("more than one slots are migrating, unknown error")
	}
	if len(slots) == 1 {
		slot := slots[0]
		if t.NewGroupId != slot.State.MigrateStatus.To || t.SlotId != slot.Id {
			return errors.Errorf("there is a migrating slot %+v, finish it first", slot)
		}
	}
	return nil
}
