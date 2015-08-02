// Copyright 2014 Wandoujia Inc. All Rights Reserved.
// Licensed under the MIT (MIT-LICENSE.txt) license.

package router

import (
	"bytes"
	"hash/crc32"
	"strings"

	"github.com/wandoulabs/codis/pkg/proxy/redis"
	"github.com/wandoulabs/codis/pkg/utils/errors"
)

var charmap [128]byte

func init() {
	for i := 0; i < len(charmap); i++ {
		c := byte(i)
		if c >= 'a' && c <= 'z' {
			c = c - 'a' + 'A'
		}
		charmap[i] = c
	}
}

var (
	blacklist = make(map[string]bool)
)
// 增加的指令
// "SLOTSINFO", "SLOTSDEL", "SLOTSMGRTSLOT", "SLOTSMGRTONE", "SLOTSMGRTTAGSLOT", "SLOTSMGRTTAGONE", "SLOTSCHECK",
// proxy完成功能?
func init() {
	for _, s := range []string{
		"KEYS", "MOVE", "OBJECT", "RENAME", "RENAMENX", "SCAN", "BITOP", "MSETNX", "MIGRATE", "RESTORE",
		"BLPOP", "BRPOP", "BRPOPLPUSH", "PSUBSCRIBE", "PUBLISH", "PUNSUBSCRIBE", "SUBSCRIBE", "RANDOMKEY",
		"UNSUBSCRIBE", "DISCARD", "EXEC", "MULTI", "UNWATCH", "WATCH", "SCRIPT",
		"BGREWRITEAOF", "BGSAVE", "CLIENT", "CONFIG", "DBSIZE", "DEBUG", "FLUSHALL", "FLUSHDB",
		"LASTSAVE", "MONITOR", "SAVE", "SHUTDOWN", "SLAVEOF", "SLOWLOG", "SYNC", "TIME",
		"SLOTSINFO", "SLOTSDEL", "SLOTSMGRTSLOT", "SLOTSMGRTONE", "SLOTSMGRTTAGSLOT", "SLOTSMGRTTAGONE", "SLOTSCHECK",
	} {
		blacklist[s] = true
	}
}

func isNotAllowed(opstr string) bool {
	return blacklist[opstr]
}

var (
	ErrBadRespType = errors.New("bad resp type for command")
	ErrBadOpStrLen = errors.New("bad command length, too short or too long")
)

// 可以认为是 Request 应该遵守的规则(?)
func getOpStr(resp *redis.Resp) (string, error) {
	// 简单协议已经映射到TypeArray了
	if !resp.IsArray() || len(resp.Array) == 0 {
		return "", ErrBadRespType
	}
	// 确保每个元素都是 string 类型
	for _, r := range resp.Array {
		if r.IsBulkBytes() {
			continue
		}
		return "", ErrBadRespType
	}

	var upper [64]byte

	var op = resp.Array[0].Value
	if len(op) == 0 || len(op) > len(upper) {
		return "", ErrBadOpStrLen
	}
	// 这是要整啥..
	for i := 0; i < len(op); i++ {
		c := uint8(op[i])
		if k := int(c); k < len(charmap) {
			upper[i] = charmap[k]
		} else {
			return strings.ToUpper(string(op)), nil
		}
	}
	return string(upper[:len(op)]), nil
}

func hashSlot(key []byte) int {
	const (
		TagBeg = '{'
		TagEnd = '}'
	)
	if beg := bytes.IndexByte(key, TagBeg); beg >= 0 {
		if end := bytes.IndexByte(key[beg+1:], TagEnd); end >= 0 {
			key = key[beg+1 : beg+1+end]
		}
	}
	return int(crc32.ChecksumIEEE(key) % MaxSlotNum)
}

// 这里提到的key分组的问题！
// 这些指定都是对多个key进行逻辑运算返回一个结果, codis把指令路由到第一个key对应的机器上, 
//		其他key存不存在这台机器就不管了, 应该是有用户自己解决
func getHashKey(resp *redis.Resp, opstr string) []byte {
	var index = 1
	switch opstr {
	case "ZINTERSTORE", "ZUNIONSTORE", "EVAL", "EVALSHA":
		index = 3
	}
	if index < len(resp.Array) {
		return resp.Array[index].Value
	}
	return nil
}
