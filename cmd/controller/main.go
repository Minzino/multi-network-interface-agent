package main

import (
    "context"
    "log"
    "net/http"
    "os"
    "os/signal"
    "time"

    "multinic-agent/internal/controller"

    corev1 "k8s.io/api/core/v1"
    "k8s.io/client-go/dynamic"
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/rest"
    "k8s.io/client-go/tools/clientcmd"

    "github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
    defer cancel()

    // build kube clients (in-cluster first)
    dyn, cli := buildClients()

    ns := getenv("CONTROLLER_NAMESPACE", getenv("POD_NAMESPACE", "multinic-system"))
    agentImage := getenv("AGENT_IMAGE", "multinic-agent:latest")
    nodeCRNS := getenv("NODE_CR_NAMESPACE", ns)
    interval, _ := time.ParseDuration(getenv("POLL_INTERVAL", "15s"))
    saName := getenv("CONTROLLER_SA_NAME", getenv("SERVICE_ACCOUNT", "multinic-agent"))
    mode := getenv("CONTROLLER_MODE", "watch")
    jobTTL := getenv("CONTROLLER_JOB_TTL", "600")
    jobDelDelay := getenv("CONTROLLER_JOB_DELETE_DELAY", "0")

    c := &controller.Controller{
        Dyn:             dyn,
        Client:          cli,
        AgentImage:      agentImage,
        ImagePullPolicy: corev1.PullIfNotPresent,
        ServiceAccount:  saName,
        NodeCRNamespace: nodeCRNS,
    }
    if secs, err := time.ParseDuration(jobTTL+"s"); err == nil {
        t := int32(secs / time.Second)
        c.JobTTLSeconds = &t
    }
    if secs, err := time.ParseDuration(jobDelDelay+"s"); err == nil {
        c.JobDeleteDelaySeconds = int(secs / time.Second)
    }

    // start Prometheus metrics endpoint
    mport := getenv("CONTROLLER_METRICS_PORT", "9090")
    go func() {
        mux := http.NewServeMux()
        mux.Handle("/metrics", promhttp.Handler())
        if err := http.ListenAndServe(":"+mport, mux); err != nil {
            log.Printf("metrics server error: %v", err)
        }
    }()

    if mode == "watch" {
        w := controller.NewWatcher(c, ns)
        if err := w.Start(ctx); err != nil { log.Fatalf("watcher exited: %v", err) }
    } else {
        svc := &controller.Service{Controller: c, Namespace: ns, Interval: interval}
        if err := svc.Start(ctx); err != nil { log.Fatalf("controller exited with error: %v", err) }
    }
}

func buildClients() (dynamic.Interface, kubernetes.Interface) {
    var cfg *rest.Config
    var err error
    if cfg, err = rest.InClusterConfig(); err != nil {
        kubeconfig := os.Getenv("KUBECONFIG")
        cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
        if err != nil { log.Fatalf("kubeconfig error: %v", err) }
    }
    dyn, err := dynamic.NewForConfig(cfg)
    if err != nil { log.Fatalf("dynamic client error: %v", err) }
    cli, err := kubernetes.NewForConfig(cfg)
    if err != nil { log.Fatalf("kube client error: %v", err) }
    return dyn, cli
}

func getenv(k, def string) string { v := os.Getenv(k); if v == "" { return def }; return v }
