package scheduler

type mutex struct {
	key uintptr
}

type schedt struct {
	lock mutex
}

func lock(m *mutex) {}
