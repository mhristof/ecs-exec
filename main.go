package main

import (
	// aws sdk v2
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/adrg/xdg"
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

var Cachepath = xdg.CacheHome + "/ecs-exec.json"

func cacheAdd(key string, value string) {
	cache := map[string]string{}
	// if the file exists, load it add the value and write it back
	// if the file does not exist, create it and add the value

	// check if file exists
	_, err := os.Stat(Cachepath)
	if err != nil {
		// file does not exist, create it
		_, err = os.Create(Cachepath)
		if err != nil {
			log.Error().Err(err).Msg("Failed to create cache file")
			return
		}
	}

	// read the file
	file, err := os.OpenFile(Cachepath, os.O_RDWR, 0o644)
	if err != nil {
		log.Error().Err(err).Msg("Failed to open cache file")
		return
	}

	// decode the file
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&cache)
	if err != nil {
		log.Error().Err(err).Msg("Failed to decode cache file")
	}

	// add the value
	cache[key] = value

	// write the file
	file, err = os.OpenFile(Cachepath, os.O_RDWR, 0o644)
	if err != nil {
		log.Error().Err(err).Msg("Failed to open cache file")
		return
	}

	// close the file
	defer file.Close()

	// encode the file
	encoder := json.NewEncoder(file)
	err = encoder.Encode(cache)
	if err != nil {
		log.Error().Err(err).Msg("Failed to encode cache file")
	}

	// write the file
	err = file.Sync()
	if err != nil {
		log.Error().Err(err).Msg("Failed to sync cache file")
	}

	log.Debug().Str("key", key).Str("value", value).Str("cache", Cachepath).Msg("Added to cache")
}

func cacheGet(key string) string {
	cache := map[string]string{}
	// if the file exists, load it add the value and write it back
	// if the file does not exist, create it and add the value

	// check if file exists
	_, err := os.Stat(Cachepath)
	if err != nil {
		// file does not exist, create it
		_, err = os.Create(Cachepath)
		if err != nil {
			log.Error().Err(err).Msg("Failed to create cache file")
			return ""
		}
	}

	// read the file
	file, err := os.OpenFile(Cachepath, os.O_RDWR, 0o644)
	if err != nil {
		log.Error().Err(err).Msg("Failed to open cache file")
		return ""
	}

	// decode the file
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&cache)
	if err != nil {
		log.Error().Err(err).Msg("Failed to decode cache file")
	}

	// get the value
	value, ok := cache[key]
	if !ok {
		log.Debug().Str("key", key).Str("cache", Cachepath).Msg("Key not found in cache")
		return ""
	}

	log.Debug().Str("key", key).Str("value", value).Str("cache", Cachepath).Msg("Got from cache")
	return value
}

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
			log.Debug().Str("image", *container.Image).Str("cluster", cluster).Str("service", thisService).Str("task", task).Interface("container", container).Msg("Container details")

			shell := cacheGet(*container.Image)
			if shell == "" {
				shell := "/bin/bash"
				stdout, _, err := awsECSExec(cluster, task, *container.Name, "/bin/bash --version")
				if err != nil {
				}

				if strings.Contains(stdout, "not found") {
					shell = "/bin/sh"
				}

				cacheAdd(*container.Image, shell)
			}

			ExecMutex.Lock()
			cmdStr := fmt.Sprintf(
				"aws ecs execute-command --cluster %s --task %s --container %s --command %s --interactive",
				cluster, task, *container.Name, shell,
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

func awsECSExec(cluster, task, container, command string) (string, string, error) {
	cmdStr := fmt.Sprintf(
		"aws ecs execute-command --cluster %s --task %s --container %s --command '%s' --interactive",
		cluster, task, container, command,
	)

	log.Debug().Str("cmd", cmdStr).Msg("Executing command")

	cmd := exec.Command("bash", "-c", cmdStr)
	stdout := &strings.Builder{}
	stderr := &strings.Builder{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if err != nil {
		log.Error().Err(err).Str("stdout", stdout.String()).Str("stderr", stderr.String()).Msg("Failed to execute command")
		return "", "", err
	}

	return stdout.String(), stderr.String(), nil
}
