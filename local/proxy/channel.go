package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/yinqiwen/gsnova/common/event"
)

var ErrChannelReadTimeout = errors.New("Remote channel read timeout")
var ErrChannelAuthFailed = errors.New("Remote channel auth failed")

type ProxyChannel interface {
	Write(event.Event) (event.Event, error)
}

type RemoteProxyChannel interface {
	Open(iv uint64) error
	Closed() bool
	Request([]byte) ([]byte, error)
	ReadTimeout() time.Duration
	io.ReadWriteCloser
}

type RemoteChannel struct {
	Addr          string
	Index         int
	DirectIO      bool
	WriteJoinAuth bool
	OpenJoinAuth  bool
	HeartBeat     bool
	C             RemoteProxyChannel

	connSendedEvents uint32
	authResult       int
	iv               uint64
	wch              chan event.Event
	running          bool
}

func (rc *RemoteChannel) authed() bool {
	return rc.authResult != 0
}
func (rc *RemoteChannel) generateIV() uint64 {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	tmp := uint64(r.Int63())
	rc.iv = tmp
	return tmp
}

func (rc *RemoteChannel) Init() error {
	rc.running = true
	rc.authResult = 0

	authSession := newRandomSession()
	if !rc.DirectIO {
		rc.wch = make(chan event.Event, 5)
		go rc.processWrite()
		go rc.processRead()
	}
	if rc.HeartBeat {
		go rc.heartbeat()
	}

	start := time.Now()
	authTimeout := rc.C.ReadTimeout()
	for rc.authResult == 0 {
		if time.Now().After(start.Add(authTimeout)) {
			rc.Stop()
			return fmt.Errorf("Server:%s auth timeout after %v", rc.Addr, time.Now().Sub(start))
		}
		time.Sleep(1 * time.Millisecond)
	}
	if rc.authResult == event.ErrAuthFailed {
		rc.Stop()
		return fmt.Errorf("Server:%s auth failed.", rc.Addr)
	} else if rc.authResult == event.SuccessAuthed {
		log.Printf("Server:%s authed success.", rc.Addr)
	} else {
		return fmt.Errorf("Server:%s auth recv unexpected code:%d.", rc.Addr, rc.authResult)
	}
	closeProxySession(authSession.id)
	return nil
}
func (rc *RemoteChannel) Close() {
	c := rc.C
	if nil != c {
		c.Close()
	}
}
func (rc *RemoteChannel) Stop() {
	rc.running = false
	rc.Close()
}

func (rc *RemoteChannel) heartbeat() {
	ticker := time.NewTicker(5 * time.Second)
	for rc.running {
		select {
		case <-ticker.C:
			if !rc.C.Closed() && getProxySessionSize() > 0 {
				rc.Write(&event.HeartBeatEvent{})
			}
		}
	}
}

func (rc *RemoteChannel) processWrite() {
	readBufferEv := func(evs []event.Event) []event.Event {
		sev := <-rc.wch
		if nil != sev {
			evs = append(evs, sev)
		}
		return evs
	}
	var sendEvents []event.Event
	for rc.running {
		conn := rc.C
		if len(sendEvents) == 0 {
			if len(rc.wch) > 0 {
				for len(rc.wch) > 0 {
					sendEvents = readBufferEv(sendEvents)
				}
			} else {
				sendEvents = readBufferEv(sendEvents)
			}
		}

		if !rc.running && len(sendEvents) == 0 {
			return
		}
		if conn.Closed() {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		var buf bytes.Buffer
		civ := rc.iv
		if rc.WriteJoinAuth || (rc.connSendedEvents == 0 && rc.OpenJoinAuth) {
			auth := NewAuthEvent()
			auth.Index = int64(rc.Index)
			auth.IV = civ
			event.EncryptEvent(&buf, auth, 0)
			rc.connSendedEvents++
		}
		for _, sev := range sendEvents {
			if auth, ok := sev.(*event.AuthEvent); ok {
				if auth.IV != civ {
					log.Printf("####Got %d %d", civ, auth.IV)
				}
				auth.IV = civ
				event.EncryptEvent(&buf, sev, 0)
			} else {
				event.EncryptEvent(&buf, sev, civ)
			}
		}
		rc.connSendedEvents += uint32(len(sendEvents))

		if buf.Len() > 0 {
			start := time.Now()
			_, err := conn.Write(buf.Bytes())
			if nil != err {
				conn.Close()
				log.Printf("Failed to write tcp messgage:%v", err)
			} else {
				log.Printf("[%d]%s cost %v to write %d events.", rc.Index, rc.Addr, time.Now().Sub(start), len(sendEvents))
			}
		}
		sendEvents = nil
	}
}

func (rc *RemoteChannel) processRead() {
	for rc.running {
		conn := rc.C
		if conn.Closed() {
			if getProxySessionSize() == 0 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			rc.generateIV()
			rc.connSendedEvents = 0
			err := conn.Open(rc.iv)
			if nil != err {
				log.Printf("Channel[%d] connect %s failed:%v.", rc.Index, rc.Addr, err)
				time.Sleep(1 * time.Second)
				continue
			}
			log.Printf("Channel[%d] connect %s success.", rc.Index, rc.Addr)
			if rc.OpenJoinAuth {
				rc.Write(nil)
				// auth := NewAuthEvent()
				// auth.Index = int64(rc.Index)
				// auth.IV = rc.iv
				// rc.Write(auth)
			}
		}
		data := make([]byte, 8192)
		var buf bytes.Buffer
		for {
			n, cerr := conn.Read(data)
			buf.Write(data[0:n])
			for buf.Len() > 0 {
				err, ev := event.DecryptEvent(&buf, rc.iv)
				if nil != err {
					if err == event.EBNR {
						err = nil
					} else {
						log.Printf("Failed to decode event for reason:%v", err)
						conn.Close()
					}
					break
				}
				auth, isNotify := ev.(*event.NotifyEvent)
				if isNotify {
					if !rc.authed() {
						rc.authResult = int(auth.Code)
						continue
					}
				}
				if !rc.authed() {
					log.Printf("[ERROR]Expected auth result event for auth all connection, but got %T.", ev)
					conn.Close()
					continue
				}
				HandleEvent(ev)
			}
			if nil != cerr {
				if cerr != io.EOF && cerr != ErrChannelReadTimeout {
					log.Printf("Failed to read channel for reason:%v", cerr)
				}
				conn.Close()
				break
			}
		}
	}
}

func (rc *RemoteChannel) Request(ev event.Event) (event.Event, error) {
	var buf bytes.Buffer
	auth := NewAuthEvent()
	auth.Index = int64(rc.Index)
	auth.IV = rc.generateIV()
	event.EncryptEvent(&buf, auth, 0)
	event.EncryptEvent(&buf, ev, auth.IV)
	//event.EncodeEvent(&buf, ev)
	res, err := rc.C.Request(buf.Bytes())
	if nil != err {
		return nil, err
	}
	rbuf := bytes.NewBuffer(res)
	var rev event.Event
	err, rev = event.DecryptEvent(rbuf, auth.IV)
	if nil != err {
		return nil, err
	}
	return rev, nil
}

func (rc *RemoteChannel) Write(ev event.Event) error {
	rc.wch <- ev
	return nil
}

func (rc *RemoteChannel) WriteRaw(p []byte) (int, error) {
	return rc.C.Write(p)
}

type RemoteChannelTable struct {
	cs     []*RemoteChannel
	cursor int
	mutex  sync.Mutex
}

func (p *RemoteChannelTable) Add(c *RemoteChannel) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.cs = append(p.cs, c)
}

func (p *RemoteChannelTable) StopAll() {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	for _, c := range p.cs {
		c.Stop()
	}
	p.cs = make([]*RemoteChannel, 0)
}

func (p *RemoteChannelTable) Select() *RemoteChannel {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if len(p.cs) == 0 {
		return nil
	}
	startCursor := p.cursor
	for {
		if p.cursor >= len(p.cs) {
			p.cursor = 0
		}
		c := p.cs[p.cursor]
		p.cursor++
		if nil != c {
			return c
		}
		if p.cursor == startCursor {
			break
		}
	}
	return nil
}

func NewRemoteChannelTable() *RemoteChannelTable {
	p := new(RemoteChannelTable)
	p.cs = make([]*RemoteChannel, 0)
	return p
}
