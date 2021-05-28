package types

import "time"

var TimeLayout = time.RFC3339[:(len(time.RFC3339) - 5)]

const (
	MaxTriggers = 10
)

type Trigger struct {
	Name       string
	Qualifier  string
	CreateTime time.Time
	ModifyTime time.Time
}

type Triggers []Trigger

func (ts Triggers) Len() int {
	return len(ts)
}

func (ts Triggers) Less(i, j int) bool {
	if ts[i].ModifyTime.Before(ts[j].ModifyTime) {
		return true
	}
	if ts[i].ModifyTime.After(ts[j].ModifyTime) {
		return false
	}
	return ts[i].CreateTime.Before(ts[j].CreateTime)
}

func (ts Triggers) Swap(i, j int) {
	ts[i], ts[j] = ts[j], ts[i]
}
