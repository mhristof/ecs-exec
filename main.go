package main

import (
	// aws sdk v2
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var ExecMutex = &sync.Mutex{}

var Commit = func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		panic("Could not read build info")
	}

	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return "unknown"
}()

func main() {
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	serviceName := ""
	verbose := false

	flag.StringVar(&serviceName, "name", serviceName, "Service name")
	flag.StringVar(&serviceName, "n", serviceName, "Service name")
	flag.BoolVar(&verbose, "v", verbose, "Verbose mode")

	flag.Parse()

	if verbose {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		log.Debug().Str("version", Commit).Msg("Debug mode enabled")
	}

	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		panic(err)
	}

	// new aws client for ecs service using AWS_PROFILE
	ecsClient := ecs.NewFromConfig(cfg)

	// list all the cluster ARNs
	clusters, err := ecsClient.ListClusters(context.TODO(), &ecs.ListClustersInput{})
	if err != nil {
		panic(err)
	}

	for _, cluster := range clusters.ClusterArns {
		listTaskPaginator := ecs.NewListTasksPaginator(ecsClient, &ecs.ListTasksInput{
			Cluster: aws.String(cluster),
		})

		for listTaskPaginator.HasMorePages() {
			resp, err := listTaskPaginator.NextPage(context.TODO())
			if err != nil {
				panic(err)
			}

			wg := sync.WaitGroup{}
			wg.Add(len(resp.TaskArns))

			log.Debug().Int("tasks", len(resp.TaskArns)).Str("cluster", cluster).Msg("Found tasks")

			for _, task := range resp.TaskArns {
				go func(task string) {
					defer wg.Done()
					printTaskCmd(ecsClient, cluster, serviceName, task)
				}(task)
			}

			wg.Wait()
		}
	}
}

func printTaskCmd(ecsClient *ecs.Client, cluster, service, task string) {
	taskDetails, err := ecsClient.DescribeTasks(context.TODO(), &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   []string{task},
	})
	if err != nil {
		panic(err)
	}

	for _, taskDetail := range taskDetails.Tasks {
		serviceParts := strings.Split(*taskDetail.Group, ":")
		if len(serviceParts) != 2 {
			panic("Invalid service name " + *taskDetail.Group)
		}

		thisService := serviceParts[1]
		if service != "" && thisService != service {
			log.Debug().Str("service", thisService).Str("task", task).Msg("Skipping due to service name mismatch")
			continue
		}

		if !taskDetail.EnableExecuteCommand {
			log.Debug().Str("service", thisService).Str("task", task).Msg("Skipping due to execute command disabled")
			continue
		}

		for _, container := range taskDetail.Containers {
			log.Info().Str("service", thisService).Str("task", task).Msg("Connecting to container")
			log.Debug().Str("cluster", cluster).Str("service", thisService).Str("task", task).Interface("container", container).Msg("Container details")

			ExecMutex.Lock()
			cmdStr := fmt.Sprintf(
				"aws ecs execute-command --cluster %s --task %s --container %s --command /bin/sh --interactive",
				cluster, task, *container.Name,
			)

			cmd := exec.Command("bash", "-c", cmdStr)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			_ = cmd.Run() // add error checking
			ExecMutex.Unlock()
		}
	}
}
