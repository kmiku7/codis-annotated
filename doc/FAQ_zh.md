###Codis 是什么?

Codis 是 Wandoujia Infrastructure Team 开发的一个分布式 Redis 服务, 用户可以看成是一个无限内存的 Redis 服务, 有动态扩/缩容的能力. 对偏存储型的业务更实用, 如果你需要 SUBPUB 之类的指令, Codis 是不支持的. 时刻记住 Codis 是一个分布式存储的项目. 对于海量的 key, value不太大( <= 1M ), 随着业务扩展缓存也要随之扩展的业务场景有特效.

###使用 Codis 有什么好处?

Redis获得动态扩容/缩容的能力，增减redis实例对client完全透明、不需要重启服务，不需要业务方担心 Redis 内存爆掉的问题. 也不用担心申请太大, 造成浪费. 业务方也不需要自己维护 Redis.

Codis支持水平扩容/缩容，扩容可以直接界面的 "Auto Rebalance" 按钮，缩容只需要将要下线的实例拥有的slot迁移到其它实例，然后在界面上删除下线的group即可。

###我的服务能直接迁移到 Codis 上吗?

分两种情况: 
 
1) 原来使用 twemproxy 的用户:
可以, 使用codis项目内的redis-port工具, 可以实时的同步 twemproxy 底下的 redis 数据到你的 codis 集群上. 搞定了以后, 只需要你修改一下你的配置, 将 twemproxy 的地址改成 codis 的地址就好了. 除此之外, 你什么事情都不用做.

2) 原来使用 Redis 的用户:
如果你使用了[doc/unsupported_cmds](https://github.com/wandoulabs/codis/blob/master/doc/unsupported_cmds.md)中提到的命令，是无法直接迁移到 Codis 上的. 你需要修改你的代码, 用其他的方式实现.

###相对于twemproxy的优劣？
codis和twemproxy最大的区别有两个：一个是codis支持动态水平扩展，对client完全透明不影响服务的情况下可以完成增减redis实例的操作；一个是codis是用go语言写的并支持多线程而twemproxy用C并只用单线程。
后者又意味着：codis在多核机器上的性能会好于twemproxy；codis的最坏响应时间可能会因为GC的STW而变大，不过go1.5发布后会显著降低STW的时间；如果只用一个CPU的话go语言的性能不如C，因此在一些短连接而非长连接的场景中，整个系统的瓶颈可能变成accept新tcp连接的速度，这时codis的性能可能会差于twemproxy。

###相对于redis cluster的优劣？
redis cluster基于smart client和无中心的设计，client必须按key的哈希将请求直接发送到对应的节点。这意味着：使用官方cluster必须要等对应语言的redis driver对cluster支持的开发和不断成熟；client不能直接像单机一样使用pipeline来提高效率，想同时执行多个请求来提速必须在client端自行实现异步逻辑。
而codis因其有中心节点、基于proxy的设计，对client来说可以像对单机redis一样去操作proxy（除了一些命令不支持），还可以继续使用pipeline并且如果后台redis有多个的话速度会显著快于单redis的pipeline。同时codis使用zookeeper来作为辅助，这意味着单纯对于redis集群来说需要额外的机器搭zk，不过对于很多已经在其他服务上用了zk的公司来说这不是问题：）

###Codis 可以当队列使用吗?

可以, Codis 还是支持 LPUSH LPOP这样的指令, 但是注意, 并不是说你用了 Codis 就有了一个分布式队列. 对于单个 key, 它的 value 还是会在一台机器上, 所以, 如果你的队列特别大, 而且整个业务就用了几个 key, 那么就几乎退化成了单个 redis 了, 所以, 如果你是希望有一个队列服务, 那么我建议你:

1. List key 里存放的是任务id, 用另一个key-value pair来存放具体任务信息
2. 使用 Pull 而不是 Push, 由消费者主动拉取数据, 而不是生产者推送数据.
3. 监控好你的消费者, 当队列过长的时候, 及时报警. 
4. 可以将一个大队列拆分成多个小的队列, 放在不同的key中

###Codis 支持 MSET, MGET吗?

支持, 在比较早的版本中MSET/MGET的性能较差，甚至可能差过单机redis，但2.0开始会因为后台并发执行而比单机的mset/mget快很多。

###Codis 是多线程的吗?

这是个很有意思的话题, 由于 Redis 本身是单线程的, 跑的再欢也只能跑满一个核, 这个我们没办法, 不过 Redis 的性能足够好, 当value不是太大的时候仍然能达到很高的吞吐, 在CPU瓶颈之前更应该考虑的是带宽的瓶颈 (IO). 但是 Codis proxy 是多线程的(严格来说是 goroutine), 启动的线程数是 CPU 的核数, 是可以充分利用起多核的性能的.

###Codis 支持 CAS 吗? 支持 Lua 脚本吗?

CAS 暂时不支持, 目前只支持eval的方式来跑lua脚本，需要配合TAG使用. 

###有没有 zookeeper 的教程？

[请参考这里](http://www.juvenxu.com/2015/03/20/experiences-on-zookeeper-ops/)

###Codis的性能如何?

见Readme中的[Benchmark一节](https://github.com/wandoulabs/codis#performance-benchmark)。

###我的数据在 Codis 上是安全的吗?

首先, 安全的定义有很多个级别, Codis 并不是一个多副本的系统 (用纯内存来做多副本还是很贵的), 如果 Codis 底下的 redis 机器没有配从, 也没开 bgsave, 如果挂了, 那么最坏情况下会丢失这部分的数据, 但是集群的数据不会全失效 (即使这样的, 也比以前单点故障, 一下全丢的好...-_-|||). 如果上一种情况下配了从, 这种情况, 主挂了, 到从切上来这段时间, 客户端的部分写入会失败. 主从之前没来得及同步的小部分数据会丢失.
第二种情况, 业务短时间内爆炸性增长, 内存短时间内不可预见的暴涨(就和你用数据库磁盘满了一样), Codis还没来得及扩容, 同时数据迁移的速度小于暴涨的速度, 此时会触发 Redis 的 LRU 策略, 会淘汰老的 Key. 这种情况也是无解...不过按照现在的运维经验, 我们会尽量预分配一些 buffer, 内存使用量大概 80% 的时候, 我们就会开始扩容.

除此之外, 正常的数据迁移, 扩容缩容, 数据都是安全的. 
不过我还是建议用户把它作为 Cache 使用.

###Codis 的代码在哪? 是开源的吗?

是的, Codis 现在是豌豆荚的开源项目, 在 Github 上, Licence 是 MIT, 地址是:　https://github.com/wandoulabs/codis


###你们如何保证数据迁移的过程中多个 Proxy 不会读到老的数据 (迁移的原子性) ? 

见 [Codis 数据迁移流程](http://0xffff.me/blog/2014/11/11/codis-de-she-ji-yu-shi-xian-part-2/)

###Codis支持etcd吗 ? 

支持，请参考使用教程

###现有redis集群上有上T的数据，如何迁移到Codis上来？

为了提高 Codis 推广和部署上的效率，我们为数据迁移提供了一个叫做 [redis-port](https://github.com/wandoulabs/redis-port) 的命令行工具，它能够：

+ 静态分析 RDB 文件，包括解析以及恢复 RDB 数据到 redis
+ 从 redis 上 dump RDB 文件以及从 redis 和 codis 之间动态同步数据

###如果需要迁移现有 redis 数据到 codis，该如何操作？

+ 先搭建好 codis 集群并让 codis-proxy 正确运行起来
+ 对线上每一个 redis 实例运行一个 redis-port 来向 codis 导入数据，例如：

		for port in {6379,6380,6479,6480}; do
			nohup redis-port sync --ncpu=4 --from=redis-server:${port} \
				--target=codis-proxy:19000 > ${port}.log 2>&1 &
			sleep 5
		done
		tail -f *.log
		
	- 每个 redis-port 负责将对应的 redis 数据导入到 codis
	- 多个 redis-port 之间不互相干扰，除非多个 redis 上的 key 本身出现冲突
	- 单个 redis-port 可以将负责的数据并行迁移以提高速度，通过 --ncpu 来指定并行数
	- 导入速度受带宽以及 codis-proxy 处理速度限制(本质是大量的 slotsrestore 操作)
	
+ 完成数据迁移，在适当的时候将服务指向 Codis，并将原 redis 下线

	- 旧 redis 下线时，会导致 reids-port 链接断开，于是自动退出
		
###redis-port 是如何在线迁移数据的？

+ redis-port 本质是以 slave 的形式挂载到现有 redis 服务上去的

	1. redis 会生成 RDB DUMP 文件给作为 slave 的 redis-port
	2. redis-port 分析 RDB 文件，并拆分成 key-value 对，通过 [slotsrestore](https://github.com/wandoulabs/codis/blob/master/doc/redis_change_zh.md#slotsrestore-key1-ttl1-val1-key2-ttl2-val2-) 指令发给 codis
	3. 迁移过程中发生的修改，redis 会将这些指令在 RDB DUMP 发送完成后，再发给 redis-port，而 redis-port 收到这些指令后不作处理，而直接转发给 Codis
	
+ redis-port 处理还是很快的，参考：
	- https://github.com/sripathikrishnan/redis-rdb-tools
	- https://github.com/cupcake/rdb

### Dashboard 中 Ops 一直是 0？

检查你的启动 dashboard 进程的机器，看是否可以访问proxy的地址，对应的地址是 proxy 启动参数中的 debug_var_addr 中填写的地址。

### 如果出现错误 "Some proxies may not stop cleanly ..." 如何处理？

可能的原因是之前某个 proxy 并没有正常退出所致，我们并不能区分该 proxy 是 session expire 或者已经退出，为了安全起见，我们在这期间是禁止做集群拓扑结构变更的操作的。请确认这个 proxy 已经确实下线，或者确实进程已经被杀掉后，然后清除这个 proxy 留下的fence，使用命令：
`codis-config -c config.ini action remove-fence` 进行操作。
