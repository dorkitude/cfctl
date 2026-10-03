// Package r2 holds cfctl's R2 helpers that don't depend on Cobra: object
// listing over the REST API, operation classes and pricing, cost estimates,
// size formatting, and the S3-compatible client used for multipart uploads
// and sync.
package r2

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// Storage classes as R2 names them.
const (
	Standard         = "Standard"
	InfrequentAccess = "InfrequentAccess"
)

// Price is one storage class's price list.
type Price struct {
	// StorageGBMonth is USD per GB-month of stored data.
	StorageGBMonth float64
	// ClassAPerMillion / ClassBPerMillion are USD per million requests.
	ClassAPerMillion float64
	ClassBPerMillion float64
	// RetrievalPerGB is USD per GB read back (Infrequent Access only).
	RetrievalPerGB float64
	// Free tier (per account per month; Standard only).
	FreeStorageGB float64
	FreeClassA    float64
	FreeClassB    float64
}

// Prices is the R2 price table.
//
// source: developers.cloudflare.com/r2/pricing, checked 2026-10-03.
// Egress is free. Billable units are rounded up (partial GB-months and
// partial millions of requests bill as a whole unit). DeleteObject,
// DeleteBucket and AbortMultipartUpload are free. The free tier applies to
// Standard storage only; Infrequent Access has none.
var Prices = map[string]Price{
	Standard: {
		StorageGBMonth:   0.015,
		ClassAPerMillion: 4.50,
		ClassBPerMillion: 0.36,
		FreeStorageGB:    10,
		FreeClassA:       1_000_000,
		FreeClassB:       10_000_000,
	},
	InfrequentAccess: {
		StorageGBMonth:   0.01,
		ClassAPerMillion: 9.00,
		ClassBPerMillion: 0.90,
		RetrievalPerGB:   0.01,
	},
}

// PricingSource is shown next to cost estimates.
const PricingSource = "developers.cloudflare.com/r2/pricing, checked 2026-10-03"

// Operation classes.
const (
	ClassA    = "A"
	ClassB    = "B"
	ClassFree = "free"
)

var freeOps = map[string]bool{
	"DeleteObject": true, "DeleteObjects": true, "DeleteBucket": true, "AbortMultipartUpload": true,
}

var classAOps = map[string]bool{
	"PutObject": true, "CopyObject": true,
	"CreateMultipartUpload": true, "UploadPart": true, "UploadPartCopy": true,
	"CompleteMultipartUpload": true, "ListMultipartUploads": true, "ListParts": true,
	"ListBuckets": true, "ListObjects": true, "ListObjectsV2": true,
	"LifecycleStorageTierTransition": true,
}

// OpClass returns ClassA, ClassB, or ClassFree for an R2 action type
// (GraphQL r2OperationsAdaptiveGroups dimensions.actionType).
//
// Class A: PutObject, CopyObject, multipart (create/upload part/complete,
// list uploads/parts), List*, PutBucket*, LifecycleStorageTierTransition.
// Free: DeleteObject, DeleteBucket, AbortMultipartUpload. Everything else
// (GetObject, HeadObject, HeadBucket, Get*, ...) is Class B.
func OpClass(action string) string {
	switch {
	case freeOps[action]:
		return ClassFree
	case classAOps[action], strings.HasPrefix(action, "List"), strings.HasPrefix(action, "PutBucket"):
		return ClassA
	}
	return ClassB
}

// Usage is one month's billable usage for one storage class.
type Usage struct {
	StorageBytes   float64 // average (or current) stored bytes over the month
	ClassA, ClassB float64 // request counts
	RetrievedBytes float64 // bytes read (billed for Infrequent Access)
}

// Cost is a cost estimate for one storage class.
type Cost struct {
	Storage, ClassA, ClassB, Retrieval, Total float64
}

// GB is a decimal gigabyte, the unit R2 bills in.
const GB = 1e9

// ceilUnits rounds a billable quantity up to whole units after the free tier.
func ceilUnits(quantity, free, unit float64) float64 {
	q := quantity - free
	if q <= 0 {
		return 0
	}
	return math.Ceil(q / unit)
}

// Estimate prices one month of usage for a storage class. Partial units
// round up: 10.2 GB over the free tier bills as 11 GB-months, 1 request over
// the free tier bills as a full million.
func Estimate(class string, u Usage) Cost {
	p, ok := Prices[class]
	if !ok {
		p = Prices[Standard]
	}
	var c Cost
	c.Storage = ceilUnits(u.StorageBytes, p.FreeStorageGB*GB, GB) * p.StorageGBMonth
	c.ClassA = ceilUnits(u.ClassA, p.FreeClassA, 1e6) * p.ClassAPerMillion
	c.ClassB = ceilUnits(u.ClassB, p.FreeClassB, 1e6) * p.ClassBPerMillion
	if p.RetrievalPerGB > 0 {
		c.Retrieval = ceilUnits(u.RetrievedBytes, 0, GB) * p.RetrievalPerGB
	}
	c.Total = c.Storage + c.ClassA + c.ClassB + c.Retrieval
	return c
}

// HumanBytes formats a byte count with decimal units (R2 bills in GB).
func HumanBytes(n float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for n >= 1000 && i < len(units)-1 {
		n /= 1000
		i++
	}
	if i == 0 {
		return formatFloat(n, 0) + " B"
	}
	return formatFloat(n, 1) + " " + units[i]
}

// HumanCount formats a count with thousands separators.
func HumanCount(n float64) string {
	s := formatFloat(math.Round(n), 0)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func formatFloat(f float64, prec int) string {
	return strconv.FormatFloat(f, 'f', prec, 64)
}

// SortedKeys returns a map's keys sorted.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
