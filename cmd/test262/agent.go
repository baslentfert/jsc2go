package main

import (
	"sync"
	"time"

	"github.com/baslentfert/jsc2go/vm"
)

// $262.agent: each agent is a VM on its own goroutine. Broadcasts hand the
// agents the data block of a SharedArrayBuffer; reports flow back as strings.

type agentHub struct {
	mu      sync.Mutex
	agents  []*agent
	reports []string
	start   time.Time
	timeout time.Duration
}

type agent struct {
	v         *vm.VM
	broadcast chan broadcastMsg
}

type broadcastMsg struct {
	block vm.SharedBlock
	num   vm.Value // a number or undefined; values cannot cross agents
	ack   chan struct{}
}

func newAgentHub(timeout time.Duration) *agentHub {
	return &agentHub{start: time.Now(), timeout: timeout}
}

func (h *agentHub) monotonicNow() vm.Value {
	return vm.Number(float64(time.Since(h.start).Microseconds()) / 1000)
}

func sleepMs(c *vm.Call) vm.Value {
	ms := c.VM.ToNumber(c.Arg(0))
	if ms > 0 {
		time.Sleep(time.Duration(ms * float64(time.Millisecond)))
	}
	return vm.Undefined
}

// install defines the main thread's $262.agent methods on obj.
func (h *agentHub) install(r *vm.Realm, obj *vm.Object) {
	r.DefineFunction(obj, "start", 1, func(c *vm.Call) vm.Value {
		h.startAgent(c.VM.ToString(c.Arg(0)).String())
		return vm.Undefined
	})
	r.DefineFunction(obj, "broadcast", 2, func(c *vm.Call) vm.Value {
		block, ok := vm.SharedBlockOf(c.Arg(0))
		if !ok {
			panic(&vm.Exception{Value: vm.Str("broadcast requires a SharedArrayBuffer")})
		}
		num := vm.Undefined
		if c.Arg(1).IsNumber() {
			num = c.Arg(1)
		}
		h.mu.Lock()
		agents := append([]*agent(nil), h.agents...)
		h.mu.Unlock()
		var acks []chan struct{}
		for _, a := range agents {
			ack := make(chan struct{})
			a.broadcast <- broadcastMsg{block, num, ack}
			acks = append(acks, ack)
		}
		deadline := time.After(h.timeout)
		for _, ack := range acks {
			select {
			case <-ack:
			case <-deadline:
				return vm.Undefined
			}
		}
		return vm.Undefined
	})
	r.DefineFunction(obj, "getReport", 0, func(c *vm.Call) vm.Value {
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.reports) == 0 {
			return vm.Null
		}
		s := h.reports[0]
		h.reports = h.reports[1:]
		return vm.Str(s)
	})
	r.DefineFunction(obj, "sleep", 1, sleepMs)
	r.DefineFunction(obj, "monotonicNow", 0, func(*vm.Call) vm.Value { return h.monotonicNow() })
}

func (h *agentHub) startAgent(src string) {
	a := &agent{v: vm.New(), broadcast: make(chan broadcastMsg, 1)}
	h.mu.Lock()
	h.agents = append(h.agents, a)
	h.mu.Unlock()
	go func() {
		defer func() { recover() }() // interrupted at the end of the test
		v := a.v
		v.CanBlock = true
		v.Print = func(string) {}
		r := v.Realm()
		var callback vm.Value
		host := r.NewObject()
		ag := r.NewObject()
		host.CreateDataProperty(vm.StrKey("agent"), vm.ObjectValue(ag))
		r.GlobalObject.CreateDataProperty(vm.StrKey("$262"), vm.ObjectValue(host))
		r.DefineFunction(ag, "receiveBroadcast", 1, func(c *vm.Call) vm.Value {
			callback = c.Arg(0)
			return vm.Undefined
		})
		r.DefineFunction(ag, "report", 1, func(c *vm.Call) vm.Value {
			s := c.VM.ToString(c.Arg(0)).String()
			h.mu.Lock()
			h.reports = append(h.reports, s)
			h.mu.Unlock()
			return vm.Undefined
		})
		r.DefineFunction(ag, "leaving", 0, func(*vm.Call) vm.Value { return vm.Undefined })
		r.DefineFunction(ag, "sleep", 1, sleepMs)
		r.DefineFunction(ag, "monotonicNow", 0, func(*vm.Call) vm.Value { return h.monotonicNow() })
		if _, err := v.Eval(src, "agent"); err != nil {
			return
		}
		if v.RunJobs() != nil || !callback.IsCallable() {
			return
		}
		select {
		case msg := <-a.broadcast:
			close(msg.ack)
			sab := r.NewSharedArrayBuffer(msg.block)
			if exc := v.Try(func() { v.Call(callback, vm.Undefined, sab, msg.num) }); exc != nil {
				return
			}
			v.RunJobs()
		case <-time.After(h.timeout):
		}
	}()
}

// shutdown stops all agents.
func (h *agentHub) shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, a := range h.agents {
		a.v.Interrupt()
	}
}
