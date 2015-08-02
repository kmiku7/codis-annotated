// Copyright 2014 Wandoujia Inc. All Rights Reserved.
// Licensed under the MIT (MIT-LICENSE.txt) license.

package router

import (
	"sync"

	"github.com/wandoulabs/codis/pkg/proxy/redis"
	"github.com/wandoulabs/codis/pkg/utils/atomic2"
)

type Dispatcher interface {
	Dispatch(r *Request) error
}

type Request struct {
	// 原始的请求串?
	OpStr string
	// 请求开始的时间戳?
	Start int64

	// 解析后的请求?
	Resp *redis.Resp

	Coalesce func() error
	// 回复?
	Response struct {
		Resp *redis.Resp
		Err  error
	}

	// 这两个的区别?
	Wait *sync.WaitGroup
	slot *sync.WaitGroup

	Failed *atomic2.Bool
}
