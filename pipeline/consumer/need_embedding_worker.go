package consumer

import (
	"context"
	"sync"
	"time"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/rpc/sort/discovery/needembedding"
	"eigenflux_server/rpc/sort/discovery/queryprocessing"
)

// NeedEmbeddingWorker derives durable work from current NeedInputs. It does not
// depend on best-effort notifications from the capture HTTP request.
type NeedEmbeddingWorker struct {
	Cache    *needembedding.Cache
	Embedder needembedding.Embedder
}

func (w *NeedEmbeddingWorker) ProcessOne(ctx context.Context) (bool, error) {
	job, err := w.Cache.Claim(ctx, time.Now().UnixMilli())
	if err != nil || job.InputID == 0 {
		return false, err
	}
	work, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	in, err := job.Snapshot().ExecutionInput()
	if err == nil {
		analysis := queryprocessing.Process(queryprocessing.NeedText(in.Target.Goal, in.Target.Context), queryprocessing.Options{})
		_, err = w.Cache.Produce(work, analysis.Normalized, w.Embedder)
	}
	// Shutdown/cancellation may stop the model call; record a retry if the DB is
	// still reachable. Otherwise the bounded lease makes the job recoverable.
	finish, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer done()
	if finishErr := w.Cache.Finish(finish, job, err == nil, time.Now().UnixMilli()); finishErr != nil {
		return true, finishErr
	}
	return true, err
}

func (w *NeedEmbeddingWorker) Start(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				worked, err := w.ProcessOne(ctx)
				if err != nil && ctx.Err() == nil {
					logger.Default().Warn("need_embedding_precompute_failed", "err", err)
				}
				if worked && err == nil {
					continue
				}
				timer := time.NewTimer(5 * time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	}
	wg.Wait()
}
