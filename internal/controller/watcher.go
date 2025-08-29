package controller

import (
    "context"

    batchinformers "k8s.io/client-go/informers/batch/v1"
    informers "k8s.io/client-go/informers"
    "k8s.io/client-go/dynamic/dynamicinformer"
    "k8s.io/client-go/tools/cache"
    "log"
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

    // Wait for cache sync
    if !cache.WaitForCacheSync(stop, crInformer.HasSynced, w.JobInformer.Informer().HasSynced) {
        return context.Canceled
    }

    <-ctx.Done()
    return nil
}

// handleCR routes CR add/update to reconcile
func (w *Watcher) handleCR(obj interface{}) {
    if un, ok := obj.(interface{ GetName() string; GetNamespace() string }); ok {
        _ = w.Reconcile(context.Background(), un.GetNamespace(), un.GetName())
        return
    }
    // attempt meta access via accessor if needed (omitted for brevity)
}
