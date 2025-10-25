package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const TaskQueue = "hello"

func GreetingWorkflow(ctx workflow.Context, name string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
	})
	var result string
	err := workflow.ExecuteActivity(ctx, SayHello, name).Get(ctx, &result)
	return result, err
}

func SayHello(ctx context.Context, name string) (string, error) {
	return fmt.Sprintf("Hello, %s 👋", name), nil
}

func main() {
	c, err := client.NewClient(client.Options{})
	if err != nil {
		log.Fatalln(err)
	}
	defer c.Close()

	w := worker.New(c, TaskQueue, worker.Options{})
	w.RegisterWorkflow(GreetingWorkflow)
	w.RegisterActivity(SayHello)

	go func() { log.Fatalln(w.Run(worker.InterruptCh())) }()
	time.Sleep(time.Second)

	we, err := c.ExecuteWorkflow(context.Background(),
		client.StartWorkflowOptions{ID: "greet-" + time.Now().Format("150405"), TaskQueue: TaskQueue},
		GreetingWorkflow, "Temporal (Local)")
	if err != nil {
		log.Fatalln(err)
	}

	var result string
	_ = we.Get(context.Background(), &result)
	log.Println("Workflow result:", result)
}
