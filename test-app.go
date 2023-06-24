package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/canonical/go-dqlite/app"
	"github.com/canonical/go-dqlite/client"
	"golang.org/x/sys/unix"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	// Check if the KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT environment variables are set
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" && os.Getenv("KUBERNETES_SERVICE_PORT") == "" {
		log.Fatal("Not running inside a Kubernetes cluster")
	}
	// Get the in-cluster configuration
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err.Error())
	}

	// Create a new Kubernetes client
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err.Error())
	}

	namespace := "go-k8s-test-app"

	// Get all services with the specified label selector
	services, err := clientset.CoreV1().Services(namespace).List(context.Background(), v1.ListOptions{})
	if err != nil {
		log.Fatal(err.Error())
	}
	fmt.Printf("dqlite service: %s, IP: %s\n", services.Items[0].Name, services.Items[0].Spec.ClusterIP)

	dir := "/app/db"
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatal(fmt.Printf("%s: can't create %s", err, dir))
	}

	// Get the IP address of the pod
	podName := os.Getenv("HOSTNAME")
	pod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), podName, v1.GetOptions{})
	if err != nil {
		log.Fatal(fmt.Printf("can't get pod IP address %s", err))
	}
	address := pod.Status.PodIP + ":9001" // Unique node address

	labelSelector := "app=test-app"
	nodeCount, err := clientset.CoreV1().Pods(namespace).List(context.Background(), v1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		log.Fatal(fmt.Printf("can't get list of pods %s", err))
	}
	// Iterate over the pods and get their IP addresses
	cluster := []string{}
	if len(nodeCount.Items) > 1 {
		for _, pod := range nodeCount.Items {
			cluster = append(cluster, pod.Status.PodIP+":9001")
			fmt.Printf("Node: %s, IP: %s\n", pod.Name, pod.Status.PodIP)
		}
	}

	logFunc := func(l client.LogLevel, format string, a ...interface{}) {
		log.Printf(fmt.Sprintf("%s: %s: %s\n", address, l.String(), format), a...)
	}
	// var join *[]string
	options := []app.Option{app.WithAddress(address), app.WithCluster(cluster), app.WithLogFunc(logFunc)}
	app, err := app.New(dir, options...)
	if err != nil {
		log.Fatal(fmt.Printf("can't create new dqlite node %s", err))
	}
	if err := app.Ready(context.Background()); err != nil {
		log.Fatal(fmt.Printf("dqlite node is not ready %s", err))
	}
	db, err := app.Open(context.Background(), "test-db")
	if err != nil {
		log.Fatal(fmt.Printf("dqlite unable to open DB %s", err))
	}
	// db is a *sql.DB object
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS model (key TEXT, value TEXT, UNIQUE(key))"); err != nil {
		log.Fatal(fmt.Printf("dqlite unable to create table %s", err))
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO model(key, value) VALUES(?, ?)", "test-key", "test value"); err != nil {
		log.Fatal(fmt.Printf("dqlite unable to insert value %s", err))
	}
	result := ""
	row := db.QueryRow("SELECT value FROM model WHERE key = ?", "test-key")
	if err := row.Scan(&result); err != nil {
		log.Fatal(fmt.Printf("dqlite unable to select value %s", err))
	} else {
		log.Println(result)
	}

	ch := make(chan os.Signal, 32)
	signal.Notify(ch, unix.SIGABRT)
	signal.Notify(ch, unix.SIGINT)
	signal.Notify(ch, unix.SIGQUIT)
	signal.Notify(ch, unix.SIGTERM)

	<-ch

	db.Close()
	app.Handover(context.Background())
	app.Close()
}
