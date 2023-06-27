package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/canonical/go-dqlite"
	"github.com/canonical/go-dqlite/app"
	"github.com/canonical/go-dqlite/client"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var (
	App *app.App
	Db  *sql.DB
)

type NodeYamlStore struct {
	ID      uint64 `yaml:"ID"`
	Address string `yaml:"Address"`
	Role    int    `yaml:"Role"`
}

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

	// Get current namespace
	namespace := getCurrentNamespace()

	dir := "/app/db"
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatal(fmt.Printf("%s: can't create %s", err, dir))
	}

	// Get the IP address of the pod
	podName := os.Getenv("HOSTNAME")
	podIP := waitForPodIP(clientset, podName, namespace, 5*time.Second)
	address := podIP + ":9001" // Unique node address
	log.Println(podName, podIP)

	// Get app label from self pod
	selfPod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), podName, v1.GetOptions{})
	if err != nil {
		log.Fatal(fmt.Printf("can't get self pod %s", err))
	}
	labelSelector := fmt.Sprintf("app=%s", selfPod.Labels["app"])

	// Get all pods with app label
	nodeCount, err := clientset.CoreV1().Pods(namespace).List(context.Background(), v1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		log.Fatal(fmt.Printf("can't get list of pods %s", err))
	}

	cluster := []string{}
	if len(nodeCount.Items) > 1 {
		// Iterate over the pods and get their IP addresses
		for _, pod := range nodeCount.Items {
			if checkPort(pod.Status.PodIP, 9001) {
				cluster = append(cluster, pod.Status.PodIP+":9001")
				log.Printf("Node found: %s, IP: %s\n", pod.Name, pod.Status.PodIP)
			}
		}

		// If there is more than 1 pod - delete cluster.yaml and info.yaml files
		_, cerr := os.Stat(dir + "/cluster.yaml")
		if !os.IsNotExist(cerr) {
			err := os.Remove(dir + "/cluster.yaml")
			if err != nil {
				log.Printf("Error deleting %s/cluster.yaml %s", dir, err)
				return
			}
		}
		_, ierr := os.Stat(dir + "/info.yaml")
		if !os.IsNotExist(ierr) {
			err := os.Remove(dir + "/info.yaml")
			if err != nil {
				log.Printf("Error deleting %s/info.yaml %s", dir, err)
				return
			}
		}
	} else {
		// if there is only 1 pod (this pod itself) - use updateNodeIp() function to update IP, ID, Role
		if err := updateNodeIp(dir, address); err != nil {
			log.Fatal(fmt.Printf("can't update node IP %s", err))
		}
	}

	// Setting log function for dqlite
	logFunc := func(l client.LogLevel, format string, a ...interface{}) {
		log.Printf(fmt.Sprintf("%s: %s: %s\n", address, l.String(), format), a...)
	}

	// Building options for dqlite
	options := []app.Option{app.WithAddress(address), app.WithCluster(cluster), app.WithLogFunc(logFunc), app.WithTracing(client.LogDebug)}

	// Create new dqlite cluster
	App, Db = newCluster(dir, options)

	// db is a *sql.DB object
	if _, err := Db.Exec("CREATE TABLE IF NOT EXISTS model (key TEXT, value TEXT, UNIQUE(key))"); err != nil {
		log.Fatal(fmt.Printf("dqlite unable to create table %s", err))
	}
	if _, err := Db.Exec("INSERT OR REPLACE INTO model(key, value) VALUES(?, ?)", "test-key", "test value"); err != nil {
		log.Fatal(fmt.Printf("dqlite unable to insert value %s", err))
	}
	result := ""
	row := Db.QueryRow("SELECT value FROM model WHERE key = ?", "test-key")
	if err := row.Scan(&result); err != nil {
		log.Fatal(fmt.Printf("dqlite unable to select value %s", err))
	} else {
		log.Println(result)
	}

	// Check periodically if some node died/offline and remove it from the cluster
	go checkAndremoveDiedNodes(dir, namespace, clientset, address, options, labelSelector, 60*time.Second)

	ch := make(chan os.Signal, 32)
	signal.Notify(ch, unix.SIGABRT)
	signal.Notify(ch, unix.SIGINT)
	signal.Notify(ch, unix.SIGQUIT)
	signal.Notify(ch, unix.SIGTERM)

	sig := <-ch
	log.Printf("Received %s signal. Shutting down gracefully...\n", sig)

	Db.Close()
	App.Handover(context.Background())
	App.Close()
}

func newCluster(dir string, options []app.Option) (*app.App, *sql.DB) {
	app, err := app.New(dir, options...)
	if err != nil {
		log.Fatal(fmt.Printf("can't create new dqlite node %s", err.Error()))
	}
	if err := app.Ready(context.Background()); err != nil {
		log.Fatal(fmt.Printf("dqlite node is not ready %s", err.Error()))
	}
	db, err := app.Open(context.Background(), "test-db")
	if err != nil {
		log.Fatal(fmt.Printf("dqlite unable to open DB %s", err.Error()))
	}

	return app, db
}

func checkPort(ip string, port int) bool {
	address := fmt.Sprintf("%s:%d", ip, port)
	conn, err := net.DialTimeout("tcp", address, 1*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

func waitForPodIP(clientset *kubernetes.Clientset, podName, namespace string, timeout time.Duration) string {
	startTime := time.Now()
	for {
		pod, err := clientset.CoreV1().Pods(namespace).Get(context.TODO(), podName, v1.GetOptions{})
		if err != nil {
			return ""
		}
		if pod.Status.PodIP != "" {
			return pod.Status.PodIP
		}
		if time.Since(startTime) > timeout {
			log.Println("timeout waiting for PodIP")
			return ""
		}
		time.Sleep(1 * time.Second)
	}
}

func checkAndremoveDiedNodes(dir string, namespace string, clientset *kubernetes.Clientset, address string, options []app.Option, labelSelector string, interval time.Duration) {
	timer := time.NewTimer(interval)
	for {
		<-timer.C
		log.Println("Checking for died nodes...")
		cli, _ := App.Leader(context.Background())
		lead, _ := cli.Leader(context.Background())
		clust, _ := cli.Cluster(context.Background())
		log.Printf("Leader: %s\n", lead.Address)
		log.Printf("Cluster: \n%s\n", clust)

		nodeCount, err := clientset.CoreV1().Pods(namespace).List(context.Background(), v1.ListOptions{LabelSelector: labelSelector})
		if err != nil {
			log.Fatal(fmt.Printf("can't get list of pods %s", err))
		}

		var podsIPs, nodesIPs []string
		nodes, err := cli.Cluster(context.Background())
		if err != nil {
			log.Fatal(fmt.Printf("can't get list of nodes %s", err))
		}
		for _, node := range nodes {
			nodesIPs = append(nodesIPs, node.Address)
		}
		for _, pod := range nodeCount.Items {
			podsIPs = append(podsIPs, pod.Status.PodIP+":9001")
		}
		for _, nodeAddr := range findMissingNodes(podsIPs, nodesIPs) {
			for _, node := range nodes {
				if node.Address == nodeAddr {
					if err := cli.Remove(context.Background(), node.ID); err != nil {
						log.Printf("Unable to remove node %s from the cluster %s", nodeAddr, err)
					} else {
						log.Printf("Node %s was removed from the cluster", nodeAddr)
					}
				}
			}
		}
		timer.Reset(interval)
	}
}

func findMissingNodes(pods []string, nodes []string) []string {
	missingElements := []string{}
	elementsMap := make(map[string]bool)
	for _, element := range pods {
		elementsMap[element] = true
	}
	for _, element := range nodes {
		if _, ok := elementsMap[element]; !ok {
			missingElements = append(missingElements, element)
		}
	}

	return missingElements
}

func replaceCluster(dir string, newAddress string, newId uint64) error {
	clusteryamlpath := dir + "/cluster.yaml"
	log.Printf("Replacing IP %s and node ID %d in %s\n", newAddress, newId, clusteryamlpath)
	var newNodes []NodeYamlStore
	newCluster := NodeYamlStore{
		ID:      newId,
		Address: newAddress,
		Role:    0,
	}
	newNodes = append(newNodes, newCluster)
	updatedYamlData, err := yaml.Marshal(&newNodes)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML data: %v", err)
	}
	err = os.WriteFile(clusteryamlpath, updatedYamlData, 0644)
	if err != nil {
		return fmt.Errorf("failed to write YAML file: %v", err)
	}

	return nil
}

func replaceInfo(dir string, newAddress string, newId uint64) error {
	clusteryamlpath := dir + "/info.yaml"
	log.Printf("Replacing IP %s and node ID %d in %s\n", newAddress, newId, clusteryamlpath)
	yamlData, err := os.ReadFile(clusteryamlpath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(yamlData), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "ID:") {
			lines[i] = "ID: " + strconv.FormatUint(newId, 10)
		} else if strings.HasPrefix(line, "Address:") {
			lines[i] = "Address: " + newAddress
		}
	}
	updatedYAML := strings.Join(lines, "\n")
	err = os.WriteFile(clusteryamlpath, []byte(updatedYAML), 0644)
	if err != nil {
		return err
	}

	return nil
}

func updateNodeIp(dir string, address string) error {
	clusteryamlpath := dir + "/cluster.yaml"
	infoyamlpath := dir + "/info.yaml"
	newID := dqlite.GenerateID(address)
	if err := replaceInfo(dir, address, newID); err != nil {
		return fmt.Errorf("failed to replace IP address in %s :%v", infoyamlpath, err)
	}
	if err := replaceCluster(dir, address, newID); err != nil {
		return fmt.Errorf("failed to replace IP address in %s :%v", clusteryamlpath, err)
	}
	store, err := client.NewYamlNodeStore(clusteryamlpath)
	if err != nil {
		return fmt.Errorf("failed to create YamlNodeStore from file at %s :%v", clusteryamlpath, err)
	}
	servers, err := store.Get(context.Background())
	if err != nil {
		return fmt.Errorf("failed to retrieve NodeInfo list :%v", err)
	}
	err = dqlite.ReconfigureMembershipExt(dir, servers)
	if err != nil {
		return fmt.Errorf("failed to reconfigure membership :%v", err)
	}

	return nil
}

func getCurrentNamespace() (namespace string) {
	namespaceBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		log.Fatal("Can't get current namespace ", err)
	}

	return string(namespaceBytes)
}
