package s3api

import (
	"encoding/xml"
	"net/http"
	"time"
)

const s3Namespace = "http://s3.amazonaws.com/doc/2006-03-01/"

type Owner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type ListAllMyBucketsResult struct {
	XMLName xml.Name      `xml:"ListAllMyBucketsResult"`
	Xmlns   string        `xml:"xmlns,attr"`
	Owner   Owner         `xml:"Owner"`
	Buckets []BucketEntry `xml:"Buckets>Bucket"`
}

type BucketEntry struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
}

type LocationConstraint struct {
	XMLName xml.Name `xml:"LocationConstraint"`
	Xmlns   string   `xml:"xmlns,attr"`
	Value   string   `xml:",chardata"`
}

type ObjectEntry struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
	Owner        *Owner `xml:"Owner,omitempty"`
}

type CommonPrefix struct {
	Prefix string `xml:"Prefix"`
}

// ListBucketResult serves both ListObjects (v1) and ListObjectsV2; fields
// specific to one version are omitted when empty.
type ListBucketResult struct {
	XMLName        xml.Name       `xml:"ListBucketResult"`
	Xmlns          string         `xml:"xmlns,attr"`
	Name           string         `xml:"Name"`
	Prefix         string         `xml:"Prefix"`
	Delimiter      string         `xml:"Delimiter,omitempty"`
	EncodingType   string         `xml:"EncodingType,omitempty"`
	MaxKeys        int            `xml:"MaxKeys"`
	IsTruncated    bool           `xml:"IsTruncated"`
	Contents       []ObjectEntry  `xml:"Contents"`
	CommonPrefixes []CommonPrefix `xml:"CommonPrefixes"`

	// v1
	Marker     *string `xml:"Marker,omitempty"`
	NextMarker string  `xml:"NextMarker,omitempty"`

	// v2
	KeyCount              *int   `xml:"KeyCount,omitempty"`
	StartAfter            string `xml:"StartAfter,omitempty"`
	ContinuationToken     string `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string `xml:"NextContinuationToken,omitempty"`
}

type CopyObjectResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	Xmlns        string   `xml:"xmlns,attr"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag"`
}

type CopyPartResult struct {
	XMLName      xml.Name `xml:"CopyPartResult"`
	Xmlns        string   `xml:"xmlns,attr"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag"`
}

type InitiateMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

// CompleteMultipartUpload is the request body of CompleteMultipartUpload.
type CompleteMultipartUpload struct {
	XMLName xml.Name       `xml:"CompleteMultipartUpload"`
	Parts   []CompletePart `xml:"Part"`
}

type CompletePart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type CompleteMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

type PartEntry struct {
	PartNumber   int    `xml:"PartNumber"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
}

type ListPartsResult struct {
	XMLName              xml.Name    `xml:"ListPartsResult"`
	Xmlns                string      `xml:"xmlns,attr"`
	Bucket               string      `xml:"Bucket"`
	Key                  string      `xml:"Key"`
	UploadID             string      `xml:"UploadId"`
	Initiator            Owner       `xml:"Initiator"`
	Owner                Owner       `xml:"Owner"`
	StorageClass         string      `xml:"StorageClass"`
	PartNumberMarker     int         `xml:"PartNumberMarker"`
	NextPartNumberMarker int         `xml:"NextPartNumberMarker"`
	MaxParts             int         `xml:"MaxParts"`
	IsTruncated          bool        `xml:"IsTruncated"`
	Parts                []PartEntry `xml:"Part"`
}

type UploadEntry struct {
	Key          string `xml:"Key"`
	UploadID     string `xml:"UploadId"`
	Initiator    Owner  `xml:"Initiator"`
	Owner        Owner  `xml:"Owner"`
	StorageClass string `xml:"StorageClass"`
	Initiated    string `xml:"Initiated"`
}

type ListMultipartUploadsResult struct {
	XMLName            xml.Name      `xml:"ListMultipartUploadsResult"`
	Xmlns              string        `xml:"xmlns,attr"`
	Bucket             string        `xml:"Bucket"`
	KeyMarker          string        `xml:"KeyMarker"`
	UploadIDMarker     string        `xml:"UploadIdMarker"`
	NextKeyMarker      string        `xml:"NextKeyMarker"`
	NextUploadIDMarker string        `xml:"NextUploadIdMarker"`
	Prefix             string        `xml:"Prefix"`
	MaxUploads         int           `xml:"MaxUploads"`
	IsTruncated        bool          `xml:"IsTruncated"`
	Uploads            []UploadEntry `xml:"Upload"`
}

// DeleteRequest is the request body of DeleteObjects.
type DeleteRequest struct {
	XMLName xml.Name           `xml:"Delete"`
	Quiet   bool               `xml:"Quiet"`
	Objects []ObjectIdentifier `xml:"Object"`
}

type ObjectIdentifier struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId,omitempty"`
}

type DeleteResult struct {
	XMLName xml.Name        `xml:"DeleteResult"`
	Xmlns   string          `xml:"xmlns,attr"`
	Deleted []DeletedObject `xml:"Deleted"`
	Errors  []DeleteError   `xml:"Error"`
}

type DeletedObject struct {
	Key string `xml:"Key"`
}

type DeleteError struct {
	Key     string `xml:"Key"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

type AccessControlPolicy struct {
	XMLName           xml.Name `xml:"AccessControlPolicy"`
	Xmlns             string   `xml:"xmlns,attr"`
	Owner             Owner    `xml:"Owner"`
	AccessControlList struct {
		Grants []Grant `xml:"Grant"`
	} `xml:"AccessControlList"`
}

type Grant struct {
	Grantee struct {
		XMLNSXSI    string `xml:"xmlns:xsi,attr"`
		XSIType     string `xml:"xsi:type,attr"`
		ID          string `xml:"ID,omitempty"`
		DisplayName string `xml:"DisplayName,omitempty"`
		URI         string `xml:"URI,omitempty"`
	} `xml:"Grantee"`
	Permission string `xml:"Permission"`
}

// s3lab exposes a single fixed owner.
var defaultOwner = Owner{ID: "s3lab", DisplayName: "s3lab"}

// timeISO formats timestamps for XML bodies (S3 uses millisecond precision).
func timeISO(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// timeHTTP formats timestamps for headers such as Last-Modified.
func timeHTTP(t time.Time) string {
	return t.UTC().Format(http.TimeFormat)
}
