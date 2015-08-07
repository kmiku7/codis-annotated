// Copyright 2014 Wandoujia Inc. All Rights Reserved.
// Licensed under the MIT (MIT-LICENSE.txt) license.

package models

import (
	"encoding/json"
	"fmt"
	"path"

	"github.com/wandoulabs/codis/pkg/utils/errors"
	"github.com/wandoulabs/zkhelper"
)

type SlotStatus string

const (
	SLOT_STATUS_ONLINE      SlotStatus = "online"
	SLOT_STATUS_OFFLINE     SlotStatus = "offline"
	SLOT_STATUS_MIGRATE     SlotStatus = "migrate"
	SLOT_STATUS_PRE_MIGRATE SlotStatus = "pre_migrate"
)

var ErrSlotAlreadyExists = errors.New("slots already exists")
var ErrUnknownSlotStatus = errors.New("unknown slot status, slot status should be (online, offline, migrate, pre_migrate)")

type SlotMigrateStatus struct {
	From int `json:"from"`
	To   int `json:"to"`
}

type SlotMultiSetParam struct {
	From    int        `json:"from"`
	To      int        `json:"to"`
	Status  SlotStatus `json:"status"`
	GroupId int        `json:"group_id"`
}

type SlotState struct {
	Status        SlotStatus        `json:"status"`
	// 这个字段只有Status处于migrage/pre_migrate才有效(?)
	MigrateStatus SlotMigrateStatus `json:"migrate_status"`
	LastOpTs      string            `json:"last_op_ts"` // operation timestamp
}

// demo json:
//	{"product_name":"test","id":0,"group_id":1,"state":{"status":"online","migrate_status":{"from":-1,"to":-1},"last_op_ts":"0"}}
type Slot struct {
	ProductName string    `json:"product_name"`
	Id          int       `json:"id"`
	GroupId     int       `json:"group_id"`
	State       SlotState `json:"state"`
}

func (s *Slot) String() string {
	b, _ := json.MarshalIndent(s, "", "  ")
	return string(b)
}

func NewSlot(productName string, id int) *Slot {
	return &Slot{
		ProductName: productName,
		Id:          id,
		GroupId:     INVALID_ID,
		State: SlotState{
			Status:   SLOT_STATUS_OFFLINE,
			LastOpTs: "0",
			MigrateStatus: SlotMigrateStatus{
				From: INVALID_ID,
				To:   INVALID_ID,
			},
		},
	}
}

func GetSlotPath(productName string, slotId int) string {
	return fmt.Sprintf("/zk/codis/db_%s/slots/slot_%d", productName, slotId)
}

func GetSlotBasePath(productName string) string {
	return fmt.Sprintf("/zk/codis/db_%s/slots", productName)
}

// 获取单个slot的信息
func GetSlot(zkConn zkhelper.Conn, productName string, id int) (*Slot, error) {
	zkPath := GetSlotPath(productName, id)
	data, _, err := zkConn.Get(zkPath)
	if err != nil {
		return nil, errors.Trace(err)
	}

	var slot Slot
	if err := json.Unmarshal(data, &slot); err != nil {
		return nil, errors.Trace(err)
	}
	return &slot, nil
}

func GetMigratingSlots(conn zkhelper.Conn, productName string) ([]*Slot, error) {
	migrateSlots := make([]*Slot, 0)
	slots, err := Slots(conn, productName)
	if err != nil {
		return nil, errors.Trace(err)
	}

	for _, slot := range slots {
		if slot.State.Status == SLOT_STATUS_MIGRATE || slot.State.Status == SLOT_STATUS_PRE_MIGRATE {
			migrateSlots = append(migrateSlots, slot)
		}
	}
	return migrateSlots, nil
}

func Slots(zkConn zkhelper.Conn, productName string) ([]*Slot, error) {
	zkPath := GetSlotBasePath(productName)
	children, _, err := zkConn.Children(zkPath)
	if err != nil {
		return nil, errors.Trace(err)
	}

	var slots []*Slot
	for _, p := range children {
		data, _, err := zkConn.Get(path.Join(zkPath, p))
		if err != nil {
			return nil, errors.Trace(err)
		}
		slot := &Slot{}
		if err := json.Unmarshal(data, &slot); err != nil {
			return nil, errors.Trace(err)
		}
		slots = append(slots, slot)
	}
	return slots, nil
}

func NoGroupSlots(zkConn zkhelper.Conn, productName string) ([]*Slot, error) {
	slots, err := Slots(zkConn, productName)
	if err != nil {
		return nil, errors.Trace(err)
	}

	var ret []*Slot
	for _, slot := range slots {
		if slot.GroupId == INVALID_ID {
			ret = append(ret, slot)
		}
	}
	return ret, nil
}

func SetSlots(zkConn zkhelper.Conn, productName string, slots []*Slot, groupId int, status SlotStatus) error {
	// 这个check什么意思?
	// 这个函数批量设置slots的group和status的, 也就是说只有online/offline的slot才可以更新？
	// 迁移状态的不可以更新， 什么时候会用？
	// 只有在create server group的时候创建一次.
	if status != SLOT_STATUS_OFFLINE && status != SLOT_STATUS_ONLINE {
		return errors.Errorf("invalid status")
	}

	ok, err := GroupExists(zkConn, productName, groupId)
	if err != nil {
		return errors.Trace(err)
	}
	if !ok {
		return errors.Errorf("group %d is not found", groupId)
	}

	for _, s := range slots {
		s.GroupId = groupId
		s.State.Status = status
		data, err := json.Marshal(s)
		if err != nil {
			return errors.Trace(err)
		}

		zkPath := GetSlotPath(productName, s.Id)
		_, err = zkhelper.CreateOrUpdate(zkConn, zkPath, string(data), 0, zkhelper.DefaultFileACLs(), true)
		if err != nil {
			return errors.Trace(err)
		}
	}

	param := SlotMultiSetParam{
		From:    -1,
		To:      -1,
		GroupId: groupId,
		Status:  status,
	}

	err = NewAction(zkConn, productName, ACTION_TYPE_MULTI_SLOT_CHANGED, param, "", true)
	return errors.Trace(err)

}

// 同上, 只是指定slots的方式不同
func SetSlotRange(zkConn zkhelper.Conn, productName string, fromSlot, toSlot, groupId int, status SlotStatus) error {
	if status != SLOT_STATUS_OFFLINE && status != SLOT_STATUS_ONLINE {
		return errors.Errorf("invalid status")
	}

	ok, err := GroupExists(zkConn, productName, groupId)
	if err != nil {
		return errors.Trace(err)
	}
	if !ok {
		return errors.Errorf("group %d is not found", groupId)
	}

	for i := fromSlot; i <= toSlot; i++ {
		s, err := GetSlot(zkConn, productName, i)
		if err != nil {
			return errors.Trace(err)
		}
		s.GroupId = groupId
		s.State.Status = status
		data, err := json.Marshal(s)
		if err != nil {
			return errors.Trace(err)
		}

		zkPath := GetSlotPath(productName, i)
		_, err = zkhelper.CreateOrUpdate(zkConn, zkPath, string(data), 0, zkhelper.DefaultFileACLs(), true)
		if err != nil {
			return errors.Trace(err)
		}
	}

	param := SlotMultiSetParam{
		From:    fromSlot,
		To:      toSlot,
		GroupId: groupId,
		Status:  status,
	}
	err = NewAction(zkConn, productName, ACTION_TYPE_MULTI_SLOT_CHANGED, param, "", true)
	return errors.Trace(err)
}

// danger operation !
// slots数是固定的, 所以这里直接覆盖写(?)
// 好像时外层调用方检查slot是否存在并提供了force参数.
func InitSlotSet(zkConn zkhelper.Conn, productName string, totalSlotNum int) error {
	for i := 0; i < totalSlotNum; i++ {
		slot := NewSlot(productName, i)
		if err := slot.Update(zkConn); err != nil {
			return errors.Trace(err)
		}
	}
	return nil
}

func (s *Slot) SetMigrateStatus(zkConn zkhelper.Conn, fromGroup, toGroup int) error {
	if fromGroup < 0 || toGroup < 0 {
		return errors.Errorf("invalid group id, from %d, to %d", fromGroup, toGroup)
	}
	// wait until all proxy confirmed
	s.State.Status = SLOT_STATUS_PRE_MIGRATE
	err := s.Update(zkConn)
	if err != nil {
		return errors.Trace(err)
	}
	// connection, product_name, action_type, target, desc, need_confirm
	// 不是创建一个action节点, 而是下发执行并确认一个action
	// err 表示 action 是否被正确执行了
	err = NewAction(zkConn, s.ProductName, ACTION_TYPE_SLOT_PREMIGRATE, s, "", true)
	if err != nil {
		return errors.Trace(err)
	}
	s.State.Status = SLOT_STATUS_MIGRATE
	s.State.MigrateStatus.From = fromGroup
	s.State.MigrateStatus.To = toGroup
	s.GroupId = toGroup
	return s.Update(zkConn)
}

func (s *Slot) Update(zkConn zkhelper.Conn) error {
	// status validation
	switch s.State.Status {
	case SLOT_STATUS_MIGRATE, SLOT_STATUS_OFFLINE,
		SLOT_STATUS_ONLINE, SLOT_STATUS_PRE_MIGRATE:
		{
			// valid status, OK
		}
	default:
		{
			return errors.Trace(ErrUnknownSlotStatus)
		}
	}

	data, err := json.Marshal(s)
	if err != nil {
		return errors.Trace(err)
	}
	zkPath := GetSlotPath(s.ProductName, s.Id)
	_, err = zkhelper.CreateOrUpdate(zkConn, zkPath, string(data), 0, zkhelper.DefaultFileACLs(), true)
	if err != nil {
		return errors.Trace(err)
	}

	if s.State.Status == SLOT_STATUS_MIGRATE {
		err = NewAction(zkConn, s.ProductName, ACTION_TYPE_SLOT_MIGRATE, s, "", true)
	} else {
		err = NewAction(zkConn, s.ProductName, ACTION_TYPE_SLOT_CHANGED, s, "", true)
	}

	return errors.Trace(err)
}
