package adapter

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRepoLocksSerializeOneRepoAndNotOthers(t *testing.T) {
	var l RepoLocks
	var inA, maxA, inB atomic.Int32
	var wg sync.WaitGroup
	bothReposAtOnce := atomic.Bool{}
	for i := 0; i < 8; i++ {
		for _, repo := range []string{"a", "b"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer l.Lock(PR{Project: "PROJ", Repo: repo})()
				cur, other := &inA, &inB
				if repo == "b" {
					cur, other = &inB, &inA
				}
				n := cur.Add(1)
				if repo == "a" && n > maxA.Load() {
					maxA.Store(n)
				}
				if other.Load() > 0 {
					bothReposAtOnce.Store(true)
				}
				time.Sleep(2 * time.Millisecond)
				cur.Add(-1)
			}()
		}
	}
	wg.Wait()
	if maxA.Load() != 1 {
		t.Fatalf("PRs of one repository overlapped: %d at once", maxA.Load())
	}
	if !bothReposAtOnce.Load() {
		t.Fatal("different repositories never ran at the same time")
	}
}
