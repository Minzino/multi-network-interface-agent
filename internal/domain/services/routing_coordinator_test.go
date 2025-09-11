package services

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetOutput(io.Discard) // suppress logs in tests
	return logger
}

func TestNewRoutingCoordinator(t *testing.T) {
	logger := createTestLogger()
	rc := NewRoutingCoordinator(logger)

	assert.NotNil(t, rc)
	assert.Equal(t, logger, rc.logger)
	assert.False(t, rc.IsLocked()) // should start unlocked
}

func TestRoutingCoordinator_ExecuteWithLock_Success(t *testing.T) {
	logger := createTestLogger()
	rc := NewRoutingCoordinator(logger)

	ctx := context.Background()
	interfaceName := "multinic0"
	executed := false

	operation := func(ctx context.Context) error {
		executed = true
		// Simulate some work
		time.Sleep(10 * time.Millisecond)
		return nil
	}

	err := rc.ExecuteWithLock(ctx, interfaceName, operation)

	assert.NoError(t, err)
	assert.True(t, executed)
	assert.False(t, rc.IsLocked()) // should be unlocked after completion
}

func TestRoutingCoordinator_ExecuteWithLock_Error(t *testing.T) {
	logger := createTestLogger()
	rc := NewRoutingCoordinator(logger)

	ctx := context.Background()
	interfaceName := "multinic0"
	expectedError := errors.New("operation failed")

	operation := func(ctx context.Context) error {
		return expectedError
	}

	err := rc.ExecuteWithLock(ctx, interfaceName, operation)

	assert.Error(t, err)
	assert.Equal(t, expectedError, err)
	assert.False(t, rc.IsLocked()) // should be unlocked even after error
}

func TestRoutingCoordinator_Serialization(t *testing.T) {
	logger := createTestLogger()
	rc := NewRoutingCoordinator(logger)

	ctx := context.Background()
	
	// Track execution order
	var executionOrder []int
	var mu sync.Mutex
	
	// Counter to track concurrent executions
	var concurrentCount int32
	var maxConcurrent int32

	// Create multiple operations that will run concurrently
	numOperations := 5
	var wg sync.WaitGroup
	
	for i := 0; i < numOperations; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			
			operation := func(ctx context.Context) error {
				// Track concurrent executions
				current := atomic.AddInt32(&concurrentCount, 1)
				if current > atomic.LoadInt32(&maxConcurrent) {
					atomic.StoreInt32(&maxConcurrent, current)
				}
				
				// Record execution order
				mu.Lock()
				executionOrder = append(executionOrder, id)
				mu.Unlock()
				
				// Simulate work
				time.Sleep(20 * time.Millisecond)
				
				atomic.AddInt32(&concurrentCount, -1)
				return nil
			}
			
			err := rc.ExecuteWithLock(ctx, "multinic0", operation)
			require.NoError(t, err)
		}(i)
	}
	
	wg.Wait()
	
	// Verify serialization
	assert.Equal(t, int32(1), maxConcurrent, "Operations should be serialized, max concurrent should be 1")
	assert.Equal(t, numOperations, len(executionOrder), "All operations should complete")
	assert.False(t, rc.IsLocked(), "Lock should be released after all operations")
}

func TestRoutingCoordinator_ConcurrentDifferentInterfaces(t *testing.T) {
	logger := createTestLogger()
	rc := NewRoutingCoordinator(logger)

	ctx := context.Background()
	
	var executed1, executed2 bool
	var mu sync.Mutex
	var wg sync.WaitGroup
	
	wg.Add(2)
	
	// Even with different interface names, operations should be serialized
	// because routing table changes can affect the whole system
	go func() {
		defer wg.Done()
		operation := func(ctx context.Context) error {
			mu.Lock()
			executed1 = true
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			return nil
		}
		err := rc.ExecuteWithLock(ctx, "multinic0", operation)
		assert.NoError(t, err)
	}()
	
	go func() {
		defer wg.Done()
		operation := func(ctx context.Context) error {
			mu.Lock()
			executed2 = true
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			return nil
		}
		err := rc.ExecuteWithLock(ctx, "multinic1", operation)
		assert.NoError(t, err)
	}()
	
	wg.Wait()
	
	assert.True(t, executed1)
	assert.True(t, executed2)
	assert.False(t, rc.IsLocked())
}

func TestRoutingCoordinator_ContextCancellation(t *testing.T) {
	logger := createTestLogger()
	rc := NewRoutingCoordinator(logger)

	ctx, cancel := context.WithCancel(context.Background())
	
	// Cancel context immediately
	cancel()
	
	operation := func(ctx context.Context) error {
		// Check if context is cancelled
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}

	err := rc.ExecuteWithLock(ctx, "multinic0", operation)
	
	// Operation should still execute and return context error
	assert.Error(t, err)
	assert.Equal(t, context.Canceled, err)
}

func TestRoutingCoordinator_IsLocked(t *testing.T) {
	logger := createTestLogger()
	rc := NewRoutingCoordinator(logger)

	ctx := context.Background()
	
	// Initially should not be locked
	assert.False(t, rc.IsLocked())
	
	var lockStatusDuringOperation bool
	var wg sync.WaitGroup
	
	// Start a goroutine that will acquire the lock
	wg.Add(1)
	go func() {
		defer wg.Done()
		operation := func(ctx context.Context) error {
			// Hold the lock for a bit to allow checking
			time.Sleep(50 * time.Millisecond)
			return nil
		}
		
		err := rc.ExecuteWithLock(ctx, "multinic0", operation)
		assert.NoError(t, err)
	}()
	
	// Give the goroutine a moment to acquire the lock
	time.Sleep(10 * time.Millisecond)
	
	// Now check if locked from main thread
	lockStatusDuringOperation = rc.IsLocked()
	
	wg.Wait()
	
	// Should have been locked during operation
	assert.True(t, lockStatusDuringOperation)
	
	// Should be unlocked after operation
	assert.False(t, rc.IsLocked())
}

func TestRoutingCoordinator_LockContention(t *testing.T) {
	logger := createTestLogger()
	rc := NewRoutingCoordinator(logger)

	ctx := context.Background()
	numWorkers := 10
	operationsPerWorker := 5
	
	var completedOperations int32
	var wg sync.WaitGroup
	
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			
			for j := 0; j < operationsPerWorker; j++ {
				operation := func(ctx context.Context) error {
					// Simulate routing work
					time.Sleep(1 * time.Millisecond)
					atomic.AddInt32(&completedOperations, 1)
					return nil
				}
				
				err := rc.ExecuteWithLock(ctx, "multinic0", operation)
				require.NoError(t, err)
			}
		}(i)
	}
	
	wg.Wait()
	
	expectedOperations := int32(numWorkers * operationsPerWorker)
	assert.Equal(t, expectedOperations, completedOperations)
	assert.False(t, rc.IsLocked())
}

// Benchmark for performance testing
func BenchmarkRoutingCoordinator_ExecuteWithLock(b *testing.B) {
	logger := createTestLogger()
	rc := NewRoutingCoordinator(logger)

	ctx := context.Background()
	
	operation := func(ctx context.Context) error {
		// Minimal work
		return nil
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			err := rc.ExecuteWithLock(ctx, "multinic0", operation)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}