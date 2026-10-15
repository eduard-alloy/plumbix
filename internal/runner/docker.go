package runner

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// Labels set on every container Plumbix creates.
const (
	ManagedLabel = "plumbix.managed"
	JobLabel     = "plumbix.job"
)

// cleanupTimeout bounds the kill and remove calls made after a job ends.
const cleanupTimeout = 30 * time.Second

// Docker runs jobs through the Docker Engine API. It reads DOCKER_HOST and
// related variables, and negotiates the API version with the engine.
type Docker struct {
	cli *client.Client
}

// NewDocker connects to the Docker engine described by the environment.
func NewDocker() (*Docker, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Docker{cli: cli}, nil
}

// Close releases the client's connections.
func (d *Docker) Close() error { return d.cli.Close() }

// Run executes job in a fresh container and removes the container afterwards.
func (d *Docker) Run(ctx context.Context, job Job) (Result, error) {
	start := time.Now()
	if err := d.ensureImage(ctx, job); err != nil {
		return Result{}, err
	}

	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     containerConfig(job),
		HostConfig: hostConfig(job),
	})
	if err != nil {
		return Result{}, fmt.Errorf("create container for step %s: %w", job.Name, err)
	}
	id := created.ID
	defer d.remove(id)

	// Wait and logs use their own context so a cancelled job can still be
	// killed and drained cleanly.
	bg, stopBg := context.WithCancel(context.Background())
	defer stopBg()
	wait := d.cli.ContainerWait(bg, id, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})

	if _, err := d.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return Result{}, fmt.Errorf("start container for step %s: %w", job.Name, err)
	}

	logsDone := make(chan struct{})
	logs, err := d.cli.ContainerLogs(bg, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: true})
	if err != nil {
		close(logsDone)
	} else {
		go func() {
			defer close(logsDone)
			defer logs.Close()
			_, _ = stdcopy.StdCopy(job.Log, job.Log, logs)
		}()
	}

	timer := time.NewTimer(job.Timeout)
	defer timer.Stop()

	res := Result{ExitCode: -1}
	select {
	case w := <-wait.Result:
		res.ExitCode = int(w.StatusCode)
	case err := <-wait.Error:
		return Result{}, fmt.Errorf("wait for step %s: %w", job.Name, err)
	case <-timer.C:
		res.TimedOut = true
		d.kill(id)
		select {
		case <-wait.Result:
		case <-wait.Error:
		}
	case <-ctx.Done():
		d.kill(id)
		return Result{}, ctx.Err()
	}
	<-logsDone
	res.Duration = time.Since(start)
	return res, nil
}

func containerConfig(job Job) *container.Config {
	cfg := &container.Config{
		Image:      job.Image,
		WorkingDir: WorkspacePath,
		Labels:     map[string]string{ManagedLabel: "true", JobLabel: job.Name},
	}
	for _, k := range slices.Sorted(maps.Keys(job.Env)) {
		cfg.Env = append(cfg.Env, k+"="+job.Env[k])
	}
	if job.Script != "" {
		cfg.Cmd = []string{"sh", "-c", job.Script}
	}
	return cfg
}

func hostConfig(job Job) *container.HostConfig {
	init := true
	mounts := []mount.Mount{{Type: mount.TypeBind, Source: job.Workspace, Target: WorkspacePath, ReadOnly: job.WorkspaceReadOnly}}
	for _, m := range job.Mounts {
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	return &container.HostConfig{
		Mounts:      mounts,
		SecurityOpt: []string{"no-new-privileges"},
		Init:        &init,
		Resources: container.Resources{
			NanoCPUs: int64(job.CPUs * 1e9),
			Memory:   int64(job.MemoryMB) << 20,
		},
	}
}

// ensureImage pulls job.Image when it is not present locally.
func (d *Docker) ensureImage(ctx context.Context, job Job) error {
	_, err := d.cli.ImageInspect(ctx, job.Image)
	if err == nil {
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect image %s: %w", job.Image, err)
	}
	fmt.Fprintf(job.Log, "plumbix: pulling image %s\n", job.Image)
	resp, err := d.cli.ImagePull(ctx, job.Image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", job.Image, err)
	}
	defer resp.Close()
	if err := resp.Wait(ctx); err != nil {
		return fmt.Errorf("pull image %s: %w", job.Image, err)
	}
	return nil
}

// kill stops the container. Errors are ignored: the container may already
// have exited, and remove (with force) runs afterwards either way.
func (d *Docker) kill(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, _ = d.cli.ContainerKill(ctx, id, client.ContainerKillOptions{})
}

func (d *Docker) remove(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, _ = d.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
}

// managedContainers lists the job names of containers carrying the Plumbix
// label, limited to one job name unless jobName is empty. Used to verify
// cleanup, and later to recover after a crash.
func (d *Docker) managedContainers(ctx context.Context, jobName string) ([]string, error) {
	filters := client.Filters{"label": {ManagedLabel + "=true": true}}
	if jobName != "" {
		filters["label"][JobLabel+"="+jobName] = true
	}
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, c := range list.Items {
		names = append(names, c.Labels[JobLabel])
	}
	return names, nil
}
