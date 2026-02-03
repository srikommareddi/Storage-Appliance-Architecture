package nvmedrv

import "sync/atomic"

type queueManager struct {
	count  int
	cursor uint32
}

func newQueueManager(count int) *queueManager {
	if count <= 0 {
		count = 1
	}
	return &queueManager{count: count}
}

func (qm *queueManager) next() int {
	return int(atomic.AddUint32(&qm.cursor, 1)-1) % qm.count
}
