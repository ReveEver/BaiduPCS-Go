package taskframework

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-runewidth"
)

const (
	TaskWait = iota
	TaskRun
	TaskDone
	TaskFail
)

var Dot = Spinner{
	Frames: []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"},
	FPS:    time.Second / 10,
}

type TaskPrinter struct {
	wg           *sync.WaitGroup // 需确保主循环退出
	Spinner      Spinner
	ticker       *time.Ticker
	updateChan   chan interface{}
	quit         bool
	idMap        map[string]int
	tasks        []*taskState
	multiLineMsg string
	mask         []int // 记录上次每行输出宽度, 下次输出时用空格覆盖
}

type taskState struct {
	info *TaskInfo
	stat int
	msg  string
}

type taskMsg struct {
	id  string
	msg string
}

type statMsg struct {
	id   string
	stat int
}

type spinnerMsg struct{}

type quitMsg struct{}

func NewTaskPrinter() *TaskPrinter {
	r := &TaskPrinter{
		wg:         new(sync.WaitGroup),
		Spinner:    Dot,
		ticker:     nil,
		updateChan: make(chan interface{}, 1),
		idMap:      make(map[string]int),
		tasks:      nil,
		quit:       false,
	}
	return r
}

func (p *TaskPrinter) SetTaskInfo(info *TaskInfo) {
	p.idMap[info.Id()] = len(p.tasks)
	p.tasks = append(p.tasks, &taskState{
		info: info,
		stat: TaskWait,
		msg:  "初始化...",
	})
}

func (p *TaskPrinter) GetPrintFunc(id string) func(string, ...interface{}) {
	return func(format string, a ...interface{}) {
		msg := fmt.Sprintf(format, a...)
		// 去除末尾的换行
		for len(msg) >= 1 && msg[len(msg)-1] == '\n' {
			msg = msg[:len(msg)-1]
		}
		if msg == "" {
			return
		}
		p.updateChan <- taskMsg{
			id:  id,
			msg: msg,
		}
	}
}

func (p *TaskPrinter) StatChange(id string, stat int) {
	p.updateChan <- statMsg{
		id:   id,
		stat: stat,
	}
}

func (p *TaskPrinter) Start() {
	p.wg.Add(1)
	defer p.wg.Done()

	p.ticker = p.Spinner.GetTicker()
	defer p.ticker.Stop()

	for {
		p.Render()
		select {
		case <-p.ticker.C:
			p.Update(spinnerMsg{})
		case m := <-p.updateChan:
			p.Update(m)
			if p.quit {
				return
			}
		}
	}
}

func (p *TaskPrinter) Stop() {
	p.updateChan <- quitMsg{}
	close(p.updateChan)
	p.wg.Wait()
}

func (p *TaskPrinter) Update(msg interface{}) {
	switch v := msg.(type) {
	case quitMsg:
		p.quit = true
		return

	case spinnerMsg:
		p.Spinner.Update()
		return

	case statMsg:
		i, ok := p.idMap[v.id]
		if ok {
			p.tasks[i].stat = v.stat
		}
		return

	case taskMsg:
		if strings.Contains(v.msg, "\n") {
			// 多行输出，单独处理
			p.multiLineMsg = fmt.Sprintf("[%s] %s", v.id, v.msg)
			return
		}
		i, ok := p.idMap[v.id]
		if ok {
			t := p.tasks[i]
			t.msg = v.msg
		}
		return

	default:
		return
	}
}

func (p *TaskPrinter) Render() {
	var strs []string
	// 多行输出放在最上放，滚动更新
	if p.multiLineMsg != "" {
		strs = strings.Split(p.multiLineMsg, "\n")
		p.multiLineMsg = ""
	}
	strs = append(strs, "--------")
	// 每个任务的状态，固定刷新
	for _, t := range p.tasks {
		s := ""
		switch t.stat {
		case TaskWait:
			s += "-"
		case TaskRun:
			s += p.Spinner.String()
		case TaskDone:
			s += "√"
		case TaskFail:
			s += "X"
		default:
			s += " "
		}

		s += fmt.Sprintf(" [%s]", t.info.id)
		if t.msg != "" {
			s += " " + t.msg
		}
		if t.info.retry > 0 {
			s += fmt.Sprintf(" (重试: %d/%d)", t.info.retry, t.info.maxRetry)
		}
		strs = append(strs, s)
	}

	buf := ""
	newMask := make([]int, len(strs))
	if p.mask != nil {
		// 第一次输出, 无需移动光标
		buf += fmt.Sprintf("\033[%dA", len(p.tasks)+1)
	}
	for i, s := range strs {
		l := runewidth.StringWidth(s)
		newMask[i] = l
		buf += s
		// 打印空格覆盖之前的输出, 部分字符宽度统计不准(如↑), 加一点余量
		if i < len(p.mask) && l < p.mask[i]+4 {
			buf += strings.Repeat(" ", p.mask[i]-l+4)
		}
		buf += "\n"
	}
	fmt.Print(buf)
	p.mask = newMask
}

type Spinner struct {
	Frames []string
	FPS    time.Duration

	frame int
}

func (s *Spinner) GetTicker() *time.Ticker {
	return time.NewTicker(s.FPS)
}

func (s *Spinner) Update() {
	s.frame++
}

func (s *Spinner) String() string {
	return s.Frames[s.frame%len(s.Frames)]
}
