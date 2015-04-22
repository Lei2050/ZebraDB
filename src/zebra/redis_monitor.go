package main

import (
	"bytes"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	l4g "log4go"

	"redis"
)

const (
	MAX_LRANGE_INDEX       = 10000
	MAX_SLEEP_TIME         = 222
	MAX_SLEEP_INC_INTERVAL = 1
)

func RedisMonitor(addr string, index int) {
	var client *redis.Client
	if c, err := redis.Dial("tcp", addr); err != nil {
		panic(fmt.Sprintf("connect redis %v", err))
	} else {
		client = c
	}
	defer client.Close()
	reply := client.Cmd("SELECT", index)
	if reply.Err != nil {
		panic(fmt.Sprintf("SELECT %s %d error", addr, index))
	}

	var buff bytes.Buffer
	var opStat, opNum uint64

	go func() {
		ticker := time.NewTicker(time.Second * 10)
		var lastOpStat, lastOpNum uint64
		for {
			select {
			case <-ticker.C:
				tmp := atomic.LoadUint64(&opStat)
				tmpN := atomic.LoadUint64(&opNum)
				l4g.Info("op stat: %d %d", tmp-lastOpStat, tmpN-lastOpNum)
				lastOpStat, lastOpNum = tmp, tmpN
			}
		}
	}()

	sign := make(chan os.Signal, 1)
	signal.Notify(sign, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)

	count := MAX_SLEEP_TIME
	for {
		select {
		case sig := <-sign:
			//信号处理
			l4g.Info("Signal: %s", sig.String())
			return
		default:
		}
		if lr, lrerr := client.Cmd("LRANGE", "dbq", 0, MAX_LRANGE_INDEX).ListBytes(); lrerr == nil {
			lrLen := len(lr)
			if lrLen == 0 {
				if count < MAX_SLEEP_TIME {
					count += MAX_SLEEP_INC_INTERVAL
				}
				time.Sleep(time.Duration(count) * time.Millisecond)
				//l4g.Debug("sleep %d", count)
			} else {
				count = 0
				for _, info := range lr {
					WriteLevelDB(&buff, info, &opNum, &opStat)
				}
				reply := client.Cmd("LTRIM", "dbq", lrLen, -1)
				if reply.Err != nil {
					l4g.Error("LTRIM dbq %d -1 error: %s", lrLen, reply.Err.Error())
					return
				}
			}
		} else {
			l4g.Error("LRANGE dbq 0, %d error: %s", MAX_LRANGE_INDEX, lrerr.Error())
			return
		}
	}
}

func WriteLevelDB(buff *bytes.Buffer, info []byte, opNum, opStat *uint64) {
	atomic.AddUint64(opNum, 1)
	buff.Write(info)
	for buff.Len() > 0 {
		infos, err := redis.Parse(buff).ListBytes()
		if err != nil {
			l4g.Error("read reply error: %s %s", buff.Bytes(), err.Error())
			buff.Reset()
			break
		}
		ret := true
		if len(infos) < 2 {
			ret = false
		} else if bytes.Equal(infos[0], REDIS_OP_SADD) {
			ret = gDB.SAdd(infos[1:])
		} else if bytes.Equal(infos[0], REDIS_OP_SREM) {
			ret = gDB.SRem(infos[1:])
		} else if bytes.Equal(infos[0], REDIS_OP_HSET) {
			ret = gDB.HSet(infos[1:])
		} else if bytes.Equal(infos[0], REDIS_OP_HMSET) {
			ret = gDB.HMSet(infos[1:])
		} else if bytes.Equal(infos[0], REDIS_OP_HDEL) {
			ret = gDB.HDel(infos[1:])
		} else if bytes.Equal(infos[0], REDIS_OP_DEL) {
			ret = gDB.HClear(infos[1:])
		} else {
			l4g.Error("no found cmd %s", infos[0])
		}
		if !ret {
			l4g.Error("op failed: %v", infos)
			l4g.Error("op failed eary read: %q", infos)
		}
		atomic.AddUint64(opStat, 1)
	}
}
