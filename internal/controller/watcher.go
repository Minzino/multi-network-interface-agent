package controller

import (
    "context"

    batchinformers "k8s.io/client-go/informers/batch/v1"
    informers "k8s.io/client-go/informers"
    "k8s.io/client-go/dynamic/dynamicinformer"
    "k8s.io/client-go/tools/cache"
    "log"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Watcher wires informers to the Controller reconcile functions
type Watcher struct {
    Ctrl              *Controller
    Namespace         string
    CRInformerFactory dynamicinformer.DynamicSharedInformerFactory
    JobInformer       batchinformers.JobInformer
    Reconcile         func(ctx context.Context, namespace, name string) error
}

// NewWatcher creates a watcher instance
func NewWatcher(ctrl *Controller, namespace string) *Watcher {
    crInfFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(ctrl.Dyn, 0, namespace, nil)
    jobsInfFactory := informers.NewSharedInformerFactoryWithOptions(ctrl.Client, 0, informers.WithNamespace(namespace))
    w := &Watcher{
        Ctrl:              ctrl,
        Namespace:         namespace,
        CRInformerFactory: crInfFactory,
        JobInformer:       jobsInfFactory.Batch().V1().Jobs(),
    }
    w.Reconcile = ctrl.Reconcile
    return w
}

// Start begins watching CRs and Jobs and blocks until ctx is done
func (w *Watcher) Start(ctx context.Context) error {
    crInformer := w.CRInformerFactory.ForResource(nodeCRGVR).Informer()
    log.Printf("watcher starting for CRs and Jobs in ns=%s", w.Namespace)

    crInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
        AddFunc: func(obj interface{}) { log.Printf("CR add event"); w.handleCR(obj) },
        UpdateFunc: func(oldObj, newObj interface{}) { log.Printf("CR update event"); w.handleCR(newObj) },
        DeleteFunc: func(obj interface{}) { 
            log.Printf("CR delete event - about to call handleCRDelete with obj type=%T", obj)
            w.handleCRDelete(obj) 
            log.Printf("CR delete event - handleCRDelete call completed")
        },
    })

    w.JobInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
        UpdateFunc: func(oldObj, newObj interface{}) {
            // On job status change, update CR statuses
            log.Printf("Job update event - processing jobs")
            _ = w.Ctrl.ProcessJobs(context.Background(), w.Namespace)
        },
    })

    stop := make(chan struct{})
    defer close(stop)
    go w.CRInformerFactory.Start(stop)
    go w.JobInformer.Informer().Run(stop)

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

// handleCR routes CR add/update to reconcile
func (w *Watcher) handleCR(obj interface{}) {
    u := unwrap(obj)
    if u != nil { _ = w.Reconcile(context.Background(), u.GetNamespace(), u.GetName()) }
    // attempt meta access via accessor if needed (omitted for brevity)
}

// handleCRDelete cleans up any job for the node when CR is deleted
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

// unwrap supports DeletedFinalStateUnknown and returns *unstructured.Unstructured if possible
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
