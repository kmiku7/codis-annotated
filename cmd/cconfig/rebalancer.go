// Copyright 2014 Wandoujia Inc. All Rights Reserved.
// Licensed under the MIT (MIT-LICENSE.txt) license.

package main

import (
	"strconv"
	"time"

	"github.com/wandoulabs/zkhelper"

	"github.com/wandoulabs/codis/pkg/models"
	"github.com/wandoulabs/codis/pkg/utils"
	"github.com/wandoulabs/codis/pkg/utils/errors"
	"github.com/wandoulabs/codis/pkg/utils/log"
)

// 保存的一个 group 的信息
type NodeInfo struct {
	GroupId   int
	// 存储的slots
	CurSlots  []int
	// 机器(master)的最大内存配置
	MaxMemory int64
}

func getLivingNodeInfos(zkConn zkhelper.Conn) ([]*NodeInfo, error) {
	groups, err := models.ServerGroups(zkConn, globalEnv.ProductName())
	if err != nil {
		return nil, errors.Trace(err)
	}
	slots, err := models.Slots(zkConn, globalEnv.ProductName())
	// slotMap[group-id] = slots-id
	slotMap := make(map[int][]int)
	for _, slot := range slots {
		if slot.State.Status == models.SLOT_STATUS_ONLINE {
			slotMap[slot.GroupId] = append(slotMap[slot.GroupId], slot.Id)
		}
	}
	var ret []*NodeInfo
	for _, g := range groups {
		master, err := g.Master(zkConn)
		if err != nil {
			return nil, errors.Trace(err)
		}
		if master == nil {
			return nil, errors.Errorf("group %d has no master", g.Id)
		}
		out, err := utils.GetRedisConfig(master.Addr, globalEnv.Password(), "maxmemory")
		if err != nil {
			return nil, errors.Trace(err)
		}
		maxMem, err := strconv.ParseInt(out, 10, 64)
		if err != nil {
			return nil, errors.Trace(err)
		}
		if maxMem <= 0 {
			return nil, errors.Errorf("redis %s should set maxmemory", master.Addr)
		}
		node := &NodeInfo{
			GroupId:   g.Id,
			CurSlots:  slotMap[g.Id],
			MaxMemory: maxMem,
		}
		ret = append(ret, node)
	}
	cnt := 0
	for _, info := range ret {
		cnt += len(info.CurSlots)
	}
	if cnt != models.DEFAULT_SLOT_NUM {
		return nil, errors.Errorf("not all slots are online")
	}
	return ret, nil
}

func getQuotaMap(zkConn zkhelper.Conn) (map[int]int, error) {
	nodes, err := getLivingNodeInfos(zkConn)
	if err != nil {
		return nil, errors.Trace(err)
	}

	ret := make(map[int]int)
	var totalMem int64
	totalQuota := 0
	for _, node := range nodes {
		totalMem += node.MaxMemory
	}

	for _, node := range nodes {
		quota := int(models.DEFAULT_SLOT_NUM * node.MaxMemory * 1.0 / totalMem)
		ret[node.GroupId] = quota
		totalQuota += quota
	}

	// round up
	if totalQuota < models.DEFAULT_SLOT_NUM {
		for k, _ := range ret {
			// 这么个加法什么意思?
			ret[k] += models.DEFAULT_SLOT_NUM - totalQuota
			break
		}
	}

	return ret, nil
}

// experimental simple auto rebalance :)
// 实验功能, 还未在生产环境使用过(?)
// 怎么做的? 一台机器可能存储了多个slots, 怎么知道每个slots的内存使用然后进行迁移?
// 	难道这里假设所有 slots 内存占用相同??
// 请看codis项目给redis添加的指令
// 使用条件是所有 slot 都处于 online 状态.
func Rebalance() error {
	targetQuota, err := getQuotaMap(safeZkConn)
	if err != nil {
		return errors.Trace(err)
	}
	// 这个结果在 getQuotaMap() 里调用一次了。
	livingNodes, err := getLivingNodeInfos(safeZkConn)
	if err != nil {
		return errors.Trace(err)
	}
	log.Infof("start rebalance")
	// 这个 loop 问题很大啊!!!
	for _, node := range livingNodes {
		for len(node.CurSlots) > targetQuota[node.GroupId] {
			for _, dest := range livingNodes {
				if dest.GroupId != node.GroupId && len(dest.CurSlots) < targetQuota[dest.GroupId] {
					slot := node.CurSlots[len(node.CurSlots)-1]
					// create a migration task
					info := &MigrateTaskInfo{
						Delay:      0,
						SlotId:     slot,
						NewGroupId: dest.GroupId,
						Status:     MIGRATE_TASK_PENDING,
						CreateAt:   strconv.FormatInt(time.Now().Unix(), 10),
					}
					globalMigrateManager.PostTask(info)

					node.CurSlots = node.CurSlots[0 : len(node.CurSlots)-1]
					dest.CurSlots = append(dest.CurSlots, slot)
				}
			}
		}
	}
	log.Infof("rebalance tasks submit finish")
	return nil
}
