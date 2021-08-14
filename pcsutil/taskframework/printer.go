package taskframework

import (
	"fmt"
	"strings"
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
	Spinner    Spinner
	ticker     *time.Ticker
	updateChan chan interface{}
	idMap      map[string]int
	tasks      []*taskState
	quit       bool
}

type taskState struct {
	info *TaskInfo
	stat int
	msgs []string
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
		Spinner:    Dot,
		ticker:     nil,
		updateChan: make(chan interface{}),
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
		msgs: []string{"初始化..."},
	})
}

func (p *TaskPrinter) GetPrintFunc(id string) func(string, ...interface{}) {
	return func(format string, a ...interface{}) {
		p.updateChan <- taskMsg{
			id: id,
			// 替换所有的换行，防止打乱输出
			msg: strings.ReplaceAll(fmt.Sprintf(format, a...), "\n", ""),
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
	// 程序终止后还要恢复，挺麻烦的，就不隐藏了
	// HideCursor()

	strs := p.Render()
	mask := make([]int, len(strs))
	line := len(strs)
	for i, s := range strs {
		l := runewidth.StringWidth(s)
		mask[i] = l
		fmt.Println(s)
	}

	p.ticker = p.Spinner.GetTicker()
	for {
		if p.quit {
			break
		}
		select {
		case <-p.ticker.C:
			p.Update(spinnerMsg{})
		case m := <-p.updateChan:
			p.Update(m)
		}

		strs := p.Render()
		newMask := make([]int, len(strs))

		if line > 0 {
			LineMoveUp(line)
		}
		for i, s := range strs {
			l := runewidth.StringWidth(s)
			newMask[i] = l
			fmt.Print(s)
			// 打印空格覆盖之前的输出, 部分字符宽度统计不准(如↑), 加一点余量
			if i < len(mask) && l < mask[i]+4 {
				fmt.Print(strings.Repeat(" ", mask[i]-l+4))
			}
			fmt.Println()
		}
		line = len(strs)
		mask = newMask
	}
	if line > 0 {
		LineMoveDown(line)
	}
}

func (p *TaskPrinter) Stop() {
	p.ticker.Stop()
	p.updateChan <- quitMsg{}
	close(p.updateChan)
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
		i, ok := p.idMap[v.id]
		if ok {
			t := p.tasks[i]
			if v.msg != "" {
				t.msgs = append(t.msgs, v.msg)
			}
		}
		return

	default:
		return
	}
}

func (p *TaskPrinter) Render() (strs []string) {
	strs = append(strs, ">>> 任务开始：")
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
		if len(t.msgs) > 0 {
			s += " " + t.msgs[len(t.msgs)-1]
		}
		if t.info.retry > 0 {
			s += fmt.Sprintf(" (重试: %d/%d)", t.info.retry, t.info.maxRetry)
		}
		strs = append(strs, s)
	}
	return
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

func LineMoveUp(n int) {
	fmt.Printf("\033[%dA", n)
}

func LineMoveDown(n int) {
	fmt.Printf("\033[%dB", n)
}

func HideCursor() {
	fmt.Printf("\033[?25l")
}

func ShowCursor() {
	fmt.Printf("\033[?25h")
}
