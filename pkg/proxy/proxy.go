// Copyright 2014 Wandoujia Inc. All Rights Reserved.
// Licensed under the MIT (MIT-LICENSE.txt) license.

package proxy

import (
	"encoding/json"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	topo "github.com/wandoulabs/go-zookeeper/zk"

	"github.com/wandoulabs/codis/pkg/models"
	"github.com/wandoulabs/codis/pkg/proxy/router"
	"github.com/wandoulabs/codis/pkg/utils/log"
)

type Server struct {
	conf   *Config
	// zk?
	topo   *Topology
	info   models.ProxyInfo
	// map[slot-id] = group-id
	groups map[int]int

	lastActionSeq int

	// zk actions 目录变化事件
	evtbus   chan interface{}
	router   *router.Router
	listener net.Listener

	// mark offline 的信号
	kill chan interface{}
	wait sync.WaitGroup
	stop sync.Once
}

// debugVarAddr 指的http接口
func New(addr string, debugVarAddr string, conf *Config) *Server {
	log.Infof("create proxy with config: %+v", conf)

	proxyHost := strings.Split(addr, ":")[0]
	debugHost := strings.Split(debugVarAddr, ":")[0]

	hostname, err := os.Hostname()
	if err != nil {
		log.PanicErrorf(err, "get host name failed")
	}
	if proxyHost == "0.0.0.0" || strings.HasPrefix(proxyHost, "127.0.0.") {
		proxyHost = hostname
	}
	if debugHost == "0.0.0.0" || strings.HasPrefix(debugHost, "127.0.0.") {
		debugHost = hostname
	}

	s := &Server{conf: conf, lastActionSeq: -1, groups: make(map[int]int)}
	s.topo = NewTopo(conf.productName, conf.zkAddr, conf.fact, conf.provider, conf.zkSessionTimeout)
	s.info.Id = conf.proxyId
	s.info.State = models.PROXY_STATE_OFFLINE
	s.info.Addr = proxyHost + ":" + strings.Split(addr, ":")[1]
	s.info.DebugVarAddr = debugHost + ":" + strings.Split(debugVarAddr, ":")[1]
	s.info.Pid = os.Getpid()
	s.info.StartAt = time.Now().String()
	s.kill = make(chan interface{})

	log.Infof("proxy info = %+v", s.info)

	if l, err := net.Listen(conf.proto, addr); err != nil {
		log.PanicErrorf(err, "open listener failed")
	} else {
		s.listener = l
	}
	// 这里仅是初始化信息， 没有从zk加载slots信息
	s.router = router.NewWithAuth(conf.passwd)
	s.evtbus = make(chan interface{}, 1024)

	s.register()

	s.wait.Add(1)
	go func() {
		defer s.wait.Done()
		s.serve()
	}()
	return s
}

func (s *Server) serve() {
	defer s.close()

	// waitOnline是阻塞函数, 等待直到变成online状态
	if !s.waitOnline() {
		return
	}

	// 监控actions事件
	s.rewatchNodes()

	for i := 0; i < router.MaxSlotNum; i++ {
		s.fillSlot(i)
	}

	go func() {
		defer s.close()
		// 处理accept事件, 每个连接有独立的goroutine处理
		s.handleConns()
	}()

	// 这里处理的事件是?
	s.loopEvents()
}

func (s *Server) handleConns() {
	ch := make(chan net.Conn, 4096)
	defer close(ch)

	// accept & create session 为何要做一层 channel 交互?
	// 感觉没有必要啊, 直接 new object & go object.serve() 不就结了 
	// 这里还是起goroutine, 看起来accpet快了, 这里go的不够快还是会堵.
	// 可以利用并行性, 提升处理速度
	go func() {
		for c := range ch {
			// 该函数原型
			// func NewSessionSize(c net.Conn, auth string, bufsize int, timeout int) *Session
			// Session 对应 RedisClient
			x := router.NewSessionSize(c, s.conf.passwd, s.conf.maxBufSize, s.conf.maxTimeout)
			go x.Serve(s.router, s.conf.maxPipeline)
		}
	}()

	for {
		// 对每个连接, 放入channel里处理
		c, err := s.listener.Accept()
		if err != nil {
			return
		} else {
			ch <- c
		}
	}
}

func (s *Server) Info() models.ProxyInfo {
	return s.info
}

func (s *Server) Join() {
	s.wait.Wait()
}

func (s *Server) Close() error {
	s.close()
	s.wait.Wait()
	return nil
}

func (s *Server) close() {
	s.stop.Do(func() {
		s.listener.Close()
		if s.router != nil {
			s.router.Close()
		}
		close(s.kill)
	})
}

func (s *Server) rewatchProxy() {
	_, err := s.topo.WatchNode(path.Join(models.GetProxyPath(s.topo.ProductName), s.info.Id), s.evtbus)
	if err != nil {
		log.PanicErrorf(err, "watch node failed")
	}
}

// 监控 actions目录子节点的变化
// fmt.Sprintf("/zk/codis/db_%s/actions", productName)
func (s *Server) rewatchNodes() []string {
	nodes, err := s.topo.WatchChildren(models.GetWatchActionPath(s.topo.ProductName), s.evtbus)
	if err != nil {
		log.PanicErrorf(err, "watch children failed")
	}
	return nodes
}

func (s *Server) register() {
	// 在zk注册proxy的临时节点
	if _, err := s.topo.CreateProxyInfo(&s.info); err != nil {
		log.PanicErrorf(err, "create proxy node failed")
	}
	// 创建节点对应的fence目录, demo:
	//	/zk/codis/db_test/fence/workstation-centos-7.kmiku7.com:19000
	// 什么用途?
	// proxy()创建的临时节点, fence()创建的永久节点, 可以监控出proxy是否处于正常状态, 有没有hang住, 有没有正常退出.
	if _, err := s.topo.CreateProxyFenceNode(&s.info); err != nil {
		log.PanicErrorf(err, "create fence node failed")
	}
}

func (s *Server) markOffline() {
	s.topo.Close(s.info.Id)
	s.info.State = models.PROXY_STATE_MARK_OFFLINE
}

func (s *Server) waitOnline() bool {
	// 这里是循环等待变为online, 共有三个状态mark_offline/offline/online
	// 见models/proxy.go
	for {
		info, err := s.topo.GetProxyInfo(s.info.Id)
		if err != nil {
			log.PanicErrorf(err, "get proxy info failed: %s", s.info.Id)
		}
		switch info.State {
		case models.PROXY_STATE_MARK_OFFLINE:
			log.Infof("mark offline, proxy got offline event: %s", s.info.Id)
			// offline到底是要变成什么样的状态?
			s.markOffline()
			return false
		case models.PROXY_STATE_ONLINE:
			s.info.State = info.State
			log.Infof("we are online: %s", s.info.Id)
			s.rewatchProxy()
			return true
		}
		select {
		case <-s.kill:
			log.Infof("mark offline, proxy is killed: %s", s.info.Id)
			s.markOffline()
			return false
		default:
		}
		log.Infof("wait to be online: %s", s.info.Id)
		time.Sleep(3 * time.Second)
	}
}

func getEventPath(evt interface{}) string {
	return evt.(topo.Event).Path
}

func needResponse(receivers []string, self models.ProxyInfo) bool {
	var info models.ProxyInfo
	for _, v := range receivers {
		err := json.Unmarshal([]byte(v), &info)
		if err != nil {
			// 出错的情况下, 可能就是保存了 proxy-id 而已
			if v == self.Id {
				return true
			}
			return false
		}
		// 解析成功
		if info.Id == self.Id && info.Pid == self.Pid && info.StartAt == self.StartAt {
			return true
		}
	}
	return false
}

func groupMaster(groupInfo models.ServerGroup) string {
	var master string
	for _, server := range groupInfo.Servers {
		if server.Type == models.SERVER_TYPE_MASTER {
			if master != "" {
				log.Panicf("two master not allowed: %+v", groupInfo)
			}
			master = server.Addr
		}
	}
	if master == "" {
		log.Panicf("master not found: %+v", groupInfo)
	}
	return master
}

func (s *Server) resetSlot(i int) {
	s.router.ResetSlot(i)
}

func (s *Server) fillSlot(i int) {
	slotInfo, slotGroup, err := s.topo.GetSlotByIndex(i)
	if err != nil {
		log.PanicErrorf(err, "get slot by index failed", i)
	}

	// 正在迁移过程中的话，slots-info保存的是新server-group，原group保存在from字段中。
	var from string
	var addr = groupMaster(*slotGroup)
	if slotInfo.State.Status == models.SLOT_STATUS_MIGRATE {
		fromGroup, err := s.topo.GetGroup(slotInfo.State.MigrateStatus.From)
		if err != nil {
			log.PanicErrorf(err, "get migrate from failed")
		}
		from = groupMaster(*fromGroup)
		if from == addr {
			log.Panicf("set slot %04d migrate from %s to %s", i, from, addr)
		}
	}

	s.groups[i] = slotInfo.GroupId
	s.router.FillSlot(i, addr, from,
		slotInfo.State.Status == models.SLOT_STATUS_PRE_MIGRATE)
}

// 设为offline则在proxy映射关系里移除
func (s *Server) onSlotRangeChange(param *models.SlotMultiSetParam) {
	log.Infof("slotRangeChange %+v", param)
	for i := param.From; i <= param.To; i++ {
		switch param.Status {
		case models.SLOT_STATUS_OFFLINE:
			s.resetSlot(i)
		case models.SLOT_STATUS_ONLINE:
			s.fillSlot(i)
		default:
			log.Panicf("can not handle status %v", param.Status)
		}
	}
}

func (s *Server) onGroupChange(groupId int) {
	log.Infof("group changed %d", groupId)
	for i, g := range s.groups {
		if g == groupId {
			s.fillSlot(i)
		}
	}
}

func (s *Server) responseAction(seq int64) {
	log.Infof("send response seq = %d", seq)
	err := s.topo.DoResponse(int(seq), &s.info)
	if err != nil {
		log.InfoErrorf(err, "send response seq = %d failed", seq)
	}
}

// 这个技巧怎么说呢... 写个注释文档之类的行不
func (s *Server) getActionObject(seq int, target interface{}) {
	act := &models.Action{Target: target}
	err := s.topo.GetActionWithSeqObject(int64(seq), act)
	if err != nil {
		log.PanicErrorf(err, "get action object failed, seq = %d", seq)
	}
	log.Infof("action %+v", act)
}

// 可能处理的各种指令
func (s *Server) checkAndDoTopoChange(seq int) bool {
	// 这里是要拿 action 里存储的 data 的
	act, err := s.topo.GetActionWithSeq(int64(seq))
	if err != nil { //todo: error is not "not exist"
		log.PanicErrorf(err, "action failed, seq = %d", seq)
	}

	// <del>判断 receiver 里是否有自己.</del>
	// 判断是不是发给自己的
	if !needResponse(act.Receivers, s.info) { //no need to response
		return false
	}

	log.Warnf("action %v receivers %v", seq, act.Receivers)

	switch act.Type {
	case models.ACTION_TYPE_SLOT_MIGRATE, models.ACTION_TYPE_SLOT_CHANGED,
		models.ACTION_TYPE_SLOT_PREMIGRATE:
		// slot 迁移
		slot := &models.Slot{}
		// 这里是拿要迁移的 slot-id
		s.getActionObject(seq, slot)
		// 迁移详细信息在  /zk/path/to/slot-id 里保存了， 再取一次
		// 本机拿到了迁移的信息, 接下来怎么处理? 哪里驱动以key为粒度的迁移行为?
		s.fillSlot(slot.Id)

	case models.ACTION_TYPE_SERVER_GROUP_CHANGED:
		serverGroup := &models.ServerGroup{}
		s.getActionObject(seq, serverGroup)
		s.onGroupChange(serverGroup.Id)

	case models.ACTION_TYPE_SERVER_GROUP_REMOVE:
		//do not care

	case models.ACTION_TYPE_MULTI_SLOT_CHANGED:
		// 见 pkg/models/slot.go SetSlotRange()
		param := &models.SlotMultiSetParam{}
		s.getActionObject(seq, param)
		s.onSlotRangeChange(param)

	default:
		log.Panicf("unknown action %+v", act)
	}
	return true
}

func (s *Server) processAction(e interface{}) {
	// 会处理两种事件, proxy status 变化
	// 新 action 事件
	if strings.Index(getEventPath(e), models.GetProxyPath(s.topo.ProductName)) == 0 {
		info, err := s.topo.GetProxyInfo(s.info.Id)
		if err != nil {
			log.PanicErrorf(err, "get proxy info failed: %s", s.info.Id)
		}
		switch info.State {
		case models.PROXY_STATE_MARK_OFFLINE:
			log.Infof("mark offline, proxy got offline event: %s", s.info.Id)
			s.markOffline()
		case models.PROXY_STATE_ONLINE:
			s.rewatchProxy()
		default:
			log.Panicf("unknown proxy state %v", info)
		}
		return
	}

	//re-watch
	// 这里返回的nodes是纯数字的?
	nodes := s.rewatchNodes()

	seqs, err := models.ExtraSeqList(nodes)
	if err != nil {
		log.PanicErrorf(err, "get seq list failed")
	}

	if len(seqs) == 0 || !s.topo.IsChildrenChangedEvent(e) {
		return
	}

	//get last pos
	// 排过序的, 找第一个大于的index
	index := -1
	for i, seq := range seqs {
		if s.lastActionSeq < seq {
			index = i
			break
		}
	}

	if index < 0 {
		return
	}

	actions := seqs[index:]
	for _, seq := range actions {
		exist, err := s.topo.Exist(path.Join(s.topo.GetActionResponsePath(seq), s.info.Id))
		if err != nil {
			log.PanicErrorf(err, "get action failed")
		}
		// 应答过了
		if exist {
			continue
		}
		// 没有应答过
		if s.checkAndDoTopoChange(seq) {
			s.responseAction(int64(seq))
		}
	}

	s.lastActionSeq = seqs[len(seqs)-1]
}

func (s *Server) loopEvents() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var tick int = 0
	for s.info.State == models.PROXY_STATE_ONLINE {
		select {
		// offline 事件
		// 事件源是?
		case <-s.kill:
			log.Infof("mark offline, proxy is killed: %s", s.info.Id)
			s.markOffline()
		// zk 事件
		// 仅仅是为了序列化吗?
		case e := <-s.evtbus:
			evtPath := getEventPath(e)
			log.Infof("got event %s, %v, lastActionSeq %d", s.info.Id, e, s.lastActionSeq)
			if strings.Index(evtPath, models.GetActionResponsePath(s.conf.productName)) == 0 {
				seq, err := strconv.Atoi(path.Base(evtPath))
				if err != nil {
					log.ErrorErrorf(err, "parse action seq failed")
				} else {
					if seq < s.lastActionSeq {
						log.Infof("ignore seq = %d", seq)
						continue
					}
				}
			}
			s.processAction(e)
		// 定时时间, 探活(?)
		// 探活粒度是秒
		case <-ticker.C:
			if maxTick := s.conf.pingPeriod; maxTick != 0 {
				if tick++; tick >= maxTick {
					s.router.KeepAlive()
					tick = 0
				}
			}
		}
	}
}
