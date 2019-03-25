package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/command/image/build"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/idtools"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/pkg/errors"
	"golang.org/x/sync/syncmap"
)

func main() {
	err := run()
	if err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cln, err := client.NewEnvClient()
	if err != nil {
		errors.Wrap(err, "failed to get new env client")
	}

	ctx := context.Background()

	err = bootstrap(ctx, cln, BootstrapConfig{
		Ref:       "busybox",
		NumImages: 1000,
	})
	if err != nil {
		return errors.Wrap(err, "failed to bootstrap")
	}

	err = stress(ctx, cln, StressConfig{
		Ref:           "busybox",
		NumBenchmarks: 10,
		NumTags:       1000,
		NumBuilds:     100,
	})
	if err != nil {
		return errors.Wrap(err, "failed to stress")
	}

	return nil
}

type BootstrapConfig struct {
	Ref       string
	NumImages int
}

func bootstrap(ctx context.Context, cln client.CommonAPIClient, cfg BootstrapConfig) error {
	log.Println("Start bootstrapping")
	rc, err := cln.ImagePull(ctx, cfg.Ref, types.ImagePullOptions{})
	if err != nil {
		return errors.Wrapf(err, "failed to pull %q", cfg.Ref)
	}
	defer rc.Close()

	err = jsonmessage.DisplayJSONMessagesToStream(rc, command.NewOutStream(os.Stdout), nil)
	if err != nil {
		return errors.Wrap(err, "failed to display pull")
	}

	pool := NewWorkerPool(100, cfg.NumImages)
	defer close(pool.Done)

	var wg sync.WaitGroup
	err = bench(func() error {
		wg.Add(cfg.NumImages)
		log.Printf("Tagging %d images", cfg.NumImages)
		for i := 0; i < cfg.NumImages; i++ {
			i := i
			pool.Jobs <- Job{
				Type: "ImageTags",
				Run: func() error {
					defer wg.Done()

					ref := fmt.Sprintf("image-%d", i)
					err := cln.ImageTag(ctx, cfg.Ref, ref)
					if err != nil {
						return errors.Wrapf(err, "failed to tag %s as %s", cfg.Ref, ref)
					}

					return nil
				},
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	wg.Wait()
	log.Println("Finished bootstrapping")
	return nil
}

type StressConfig struct {
	Ref             string
	NumBenchmarks   int
	NumTags         int
	NumBuilds       int
	NumImageRemoves int
}

func stress(ctx context.Context, cln client.CommonAPIClient, cfg StressConfig) error {
	log.Println("Start stress testing")

	pool := NewWorkerPool(100, cfg.NumTags+cfg.NumBuilds+cfg.NumImageRemoves)
	defer close(pool.Done)

	var wg sync.WaitGroup
	go func() {
		wg.Add(cfg.NumTags)
		for i := 0; i < cfg.NumTags; i++ {
			i := i
			pool.Jobs <- Job{
				Type: "ImageTags",
				Run: func() error {
					defer wg.Done()

					ref := fmt.Sprintf("stress-tag-%d", i)
					err := cln.ImageTag(ctx, cfg.Ref, ref)
					if err != nil {
						return errors.Wrapf(err, "failed to tag %s as %s", cfg.Ref, ref)
					}

					return nil
				},
			}
		}
	}()

	go func() {
		wg.Add(cfg.NumBuilds)
		for i := 0; i < cfg.NumBuilds; i++ {
			i := i
			pool.Jobs <- Job{
				Type: "ImageBuilds",
				Run: func() error {
					defer wg.Done()
					return ImageBuild(ctx, cln, i)
				},
			}
		}
	}()

	for i := 0; i < cfg.NumBenchmarks; i++ {
		time.Sleep(time.Second)
		err := bench(func() error {
			log.Println("--- Jobs summary ---")
			log.Printf("%s", pool)
			log.Println("--- end ---")

			images, err := cln.ImageList(ctx, types.ImageListOptions{})
			if err != nil {
				return errors.Wrap(err, "failed to image list")
			}

			tagSet := make(map[string]struct{})
			for _, image := range images {
				for _, tag := range image.RepoTags {
					tagSet[tag] = struct{}{}
				}
			}

			log.Printf("Found %d image tags", len(tagSet))

			return nil
		})
		if err != nil {
			return errors.Wrapf(err, "failed to bench at %d iteration", i)
		}
	}

	log.Println("Finished stress testing")
	return nil
}

func bench(f func() error) error {
	log.Println("--- benchmarking ---")
	startTime := time.Now()
	defer func() {
		endTime := time.Now()
		diff := endTime.Sub(startTime)
		log.Printf("--- %s ---", diff)
	}()

	return f()
}

type Job struct {
	Type string
	Run  func() error
}

type WorkerPool struct {
	Jobs chan Job
	Done chan struct{}

	numJobsByType *syncmap.Map
}

func NewWorkerPool(numWorkers, numJobQueue int) *WorkerPool {
	jobs := make(chan Job, numJobQueue)
	done := make(chan struct{})

	numJobsByType := new(syncmap.Map)

	for i := 0; i < numWorkers; i++ {
		go func(id int) {
			for {
				select {
				case <-done:
					return

				case job := <-jobs:
					var counter int64
					val, _ := numJobsByType.LoadOrStore(job.Type, &counter)
					counterAddr := val.(*int64)
					atomic.AddInt64(counterAddr, 1)

					err := job.Run()
					if err != nil {
						log.Fatalf("[worker %d] %s: %s", id, job.Type, err)
					}

					atomic.AddInt64(counterAddr, -1)
				}
			}
		}(i)
	}

	return &WorkerPool{
		Jobs:          jobs,
		Done:          done,
		numJobsByType: numJobsByType,
	}
}

func (wp *WorkerPool) String() string {
	var summaries []string

	for _, t := range []string{
		"ImageTags",
		"ImageBuilds",
	} {
		val, ok := wp.numJobsByType.Load(t)
		if !ok {
			continue
		}

		counterAddr := val.(*int64)
		counter := atomic.LoadInt64(counterAddr)
		summaries = append(summaries, fmt.Sprintf("%s: %d", t, counter))
	}

	return strings.Join(summaries, "\n")
}

func ImageBuild(ctx context.Context, cln client.CommonAPIClient, i int) error {
	dir, err := ioutil.TempDir("", "")
	if err != nil {
		return errors.Wrap(err, "failed to create tmp dir")
	}

	dockerfileData := []byte(fmt.Sprintf("FROM scratch\nRUN touch data-%d\n", i))
	err = ioutil.WriteFile(filepath.Join(dir, "Dockerfile"), dockerfileData, 0666)
	if err != nil {
		return errors.Wrap(err, "failed to write dockerfile")
	}

	contextDir, dockerfile, err := build.GetContextFromLocalDir(dir, "")
	if err != nil {
		return errors.Wrap(err, "failed to get context from local dir")
	}

	dockerfile, err = archive.CanonicalTarNameForPath(dockerfile)
	if err != nil {
		return errors.Wrapf(err, "cannot canonicalize dockerfile path %s", dockerfile)
	}

	buildCtx, err := archive.TarWithOptions(contextDir, &archive.TarOptions{
		ChownOpts: &idtools.IDPair{UID: 0, GID: 0},
	})
	if err != nil {
		return errors.Wrap(err, "failed to tar context dir")
	}

	ref := fmt.Sprintf("stress-build-%d", i)
	opts := types.ImageBuildOptions{
		SuppressOutput: true,
		Dockerfile:     dockerfile,
		Tags:           []string{ref},
	}

	_, err = cln.ImageBuild(ctx, buildCtx, opts)
	if err != nil {
		return errors.Wrapf(err, "failed to build image %s", contextDir)
	}

	return nil
}
