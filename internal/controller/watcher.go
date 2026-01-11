package controller

import (
    "context"

    batchinformers "k8s.io/client-go/informers/batch/v1"
    coreinformers "k8s.io/client-go/informers/core/v1"
    informers "k8s.io/client-go/informers"
    "k8s.io/client-go/dynamic/dynamicinformer"
    "k8s.io/client-go/tools/cache"
    "log"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    corev1 "k8s.io/api/core/v1"
)

// Watcher wires informers to the Controller reconcile functions
type Watcher struct {
    Ctrl              *Controller
    Namespace         string
    CRInformerFactory dynamicinformer.DynamicSharedInformerFactory
    JobInformer       batchinformers.JobInformer
    PodInformer       coreinformers.PodInformer
    Reconcile         func(ctx context.Context, namespace, name string) error
}

// NewWatcher는 CR/Job/Pod 인포머를 묶어 Watcher를 구성한다.
func NewWatcher(ctrl *Controller, namespace string) *Watcher {
    crInfFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(ctrl.Dyn, 0, namespace, nil)
    jobsInfFactory := informers.NewSharedInformerFactoryWithOptions(ctrl.Client, 0, informers.WithNamespace(namespace))
    w := &Watcher{
        Ctrl:              ctrl,
        Namespace:         namespace,
        CRInformerFactory: crInfFactory,
        JobInformer:       jobsInfFactory.Batch().V1().Jobs(),
        PodInformer:       jobsInfFactory.Core().V1().Pods(),
    }
    w.Reconcile = ctrl.Reconcile
    return w
}

// Start는 인포머를 시작하고 종료될 때까지 블록한다.
func (w *Watcher) Start(ctx context.Context) error {
    crInformer := w.CRInformerFactory.ForResource(nodeCRGVR).Informer()
    log.Printf("watcher starting for CRs and Jobs in ns=%s", w.Namespace)

    crInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
        AddFunc: func(obj interface{}) { log.Printf("CR add event"); w.handleCR(obj) },
        UpdateFunc: func(oldObj, newObj interface{}) { 
            // CR update events can be frequent - reduced logging for cleaner output
            w.handleCR(newObj) 
        },
        DeleteFunc: func(obj interface{}) { 
            log.Printf("CR delete event - about to call handleCRDelete with obj type=%T", obj)
            w.handleCRDelete(obj) 
            log.Printf("CR delete event - handleCRDelete call completed")
        },
    })

    w.JobInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
        UpdateFunc: func(oldObj, newObj interface{}) {
            // On job status change, update CR statuses (reduced logging)
            _ = w.Ctrl.ProcessJobs(context.Background(), w.Namespace)
        },
    })

    w.PodInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
        AddFunc: func(obj interface{}) { w.handlePod(obj) },
        UpdateFunc: func(oldObj, newObj interface{}) { w.handlePod(newObj) },
    })

    stop := make(chan struct{})
    defer close(stop)
    go w.CRInformerFactory.Start(stop)
    go w.JobInformer.Informer().Run(stop)
    go w.PodInformer.Informer().Run(stop)

    // initial reconcile/cleanup pass for existing CRs/Jobs
    _ = w.Ctrl.ProcessAll(context.Background(), w.Namespace)
    _ = w.Ctrl.ProcessJobs(context.Background(), w.Namespace)

    // Wait for cache sync
    if !cache.WaitForCacheSync(stop, crInformer.HasSynced, w.JobInformer.Informer().HasSynced) {
        return context.Canceled
    }

    <-ctx.Done()
    return nil
}

// handleCR은 CR add/update 이벤트를 Reconcile로 전달한다.
func (w *Watcher) handleCR(obj interface{}) {
    u := unwrap(obj)
    if u != nil { _ = w.Reconcile(context.Background(), u.GetNamespace(), u.GetName()) }
    // attempt meta access via accessor if needed (omitted for brevity)
}

// handlePod는 Pod 종료 메시지를 수집해 실패 요약을 CR에 반영한다.
func (w *Watcher) handlePod(obj interface{}) {
    pod, ok := obj.(*corev1.Pod)
    if !ok || pod == nil { return }
    // Only Multinic agent pods
    if pod.Labels["app.kubernetes.io/name"] != "multinic-agent" { return }
    // Look for terminated states
    var msg string
    var exitCode int32 = -1
    for _, cs := range pod.Status.ContainerStatuses {
        if cs.State.Terminated != nil {
            if cs.State.Terminated.Message != "" {
                msg = cs.State.Terminated.Message
            }
            exitCode = cs.State.Terminated.ExitCode
        }
    }
    if msg == "" { return }
    // Derive node/CR name from job name label or job reference
    jobName := pod.Labels["job-name"]
    nodeName := pod.Labels["multinic.io/node-name"]
    if nodeName == "" && jobName != "" {
        const prefix = "multinic-agent-"
        if len(jobName) > len(prefix) && jobName[:len(prefix)] == prefix { nodeName = jobName[len(prefix):] }
    }
    if nodeName == "" { return }
    // Only act on failure (non-zero exit) to avoid racing the success path
    if exitCode != 0 {
        _ = w.Ctrl.ApplyTerminationSummary(context.Background(), w.Namespace, nodeName, jobName, msg)
    }
}

// handleCRDelete는 CR 삭제 시 cleanup Job을 실행한다.
func (w *Watcher) handleCRDelete(obj interface{}) {
    log.Printf("handleCRDelete: FUNCTION CALLED with obj type=%T", obj)
    u := unwrap(obj)
    if u == nil { 
        log.Printf("handleCRDelete: ERROR - failed to unwrap deleted object, obj=%+v", obj)
        return 
    }
    nodeName := u.GetName()
    log.Printf("handleCRDelete: SUCCESS - unwrapped object, nodeName=%s, namespace=%s", nodeName, u.GetNamespace())
    log.Printf("handleCRDelete: attempting to launch cleanup job for node=%s", nodeName)
    // launch cleanup job to remove interfaces on CR delete
    if err := w.Ctrl.LaunchCleanupJob(context.Background(), w.Namespace, nodeName); err != nil {
        log.Printf("handleCRDelete: failed to launch cleanup job for node=%s: %v", nodeName, err)
    } else {
        log.Printf("handleCRDelete: cleanup job launch initiated for node=%s", nodeName)
    }
}

// unwrap은 DeletedFinalStateUnknown을 포함해 *unstructured.Unstructured로 변환한다.
func unwrap(obj interface{}) *unstructured.Unstructured {
    switch t := obj.(type) {
    case *unstructured.Unstructured:
        return t
    case cache.DeletedFinalStateUnknown:
        if u, ok := t.Obj.(*unstructured.Unstructured); ok { 
            return u 
        }
    }
    if u, ok := obj.(interface{ GetName() string; GetNamespace() string }); ok {
        // not strictly *unstructured.Unstructured but has name/ns accessors
        uu := &unstructured.Unstructured{}
        uu.SetName(u.GetName()); uu.SetNamespace(u.GetNamespace())
        return uu
    }
    return nil
}
