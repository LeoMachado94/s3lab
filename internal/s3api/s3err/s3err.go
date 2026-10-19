// Package s3err maps domain errors to S3 error responses.
package s3err

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"

	"github.com/LeoMachado94/s3lab/internal/s3op"
	"github.com/LeoMachado94/s3lab/internal/storage"
)

type Error struct {
	Code    string
	Message string
	Status  int
}

func (e Error) Error() string { return e.Code + ": " + e.Message }

// Coder lets other packages (auth, faults) carry an S3 error without
// importing this package.
type Coder interface {
	S3Code() string
	HTTPStatus() int
}

var (
	AccessDenied                 = Error{"AccessDenied", "Access Denied", http.StatusForbidden}
	AuthorizationHeaderMalformed = Error{"AuthorizationHeaderMalformed", "The authorization header is malformed.", http.StatusBadRequest}
	BadDigest                    = Error{"BadDigest", "The Content-MD5 you specified did not match what we received.", http.StatusBadRequest}
	BucketAlreadyOwnedByYou      = Error{"BucketAlreadyOwnedByYou", "Your previous request to create the named bucket succeeded and you already own it.", http.StatusConflict}
	BucketNotEmpty               = Error{"BucketNotEmpty", "The bucket you tried to delete is not empty.", http.StatusConflict}
	EntityTooLarge               = Error{"EntityTooLarge", "Your proposed upload exceeds the maximum allowed object size.", http.StatusBadRequest}
	EntityTooSmall               = Error{"EntityTooSmall", "Your proposed upload is smaller than the minimum allowed object size.", http.StatusBadRequest}
	IncompleteBody               = Error{"IncompleteBody", "You did not provide the number of bytes specified by the Content-Length HTTP header.", http.StatusBadRequest}
	InternalError                = Error{"InternalError", "We encountered an internal error. Please try again.", http.StatusInternalServerError}
	InvalidAccessKeyID           = Error{"InvalidAccessKeyId", "The AWS Access Key Id you provided does not exist in our records.", http.StatusForbidden}
	InvalidArgument              = Error{"InvalidArgument", "Invalid Argument", http.StatusBadRequest}
	InvalidBucketName            = Error{"InvalidBucketName", "The specified bucket is not valid.", http.StatusBadRequest}
	InvalidDigest                = Error{"InvalidDigest", "The Content-MD5 you specified is not valid.", http.StatusBadRequest}
	InvalidPart                  = Error{"InvalidPart", "One or more of the specified parts could not be found.", http.StatusBadRequest}
	InvalidPartOrder             = Error{"InvalidPartOrder", "The list of parts was not in ascending order.", http.StatusBadRequest}
	InvalidRange                 = Error{"InvalidRange", "The requested range is not satisfiable.", http.StatusRequestedRangeNotSatisfiable}
	InvalidRequest               = Error{"InvalidRequest", "Invalid Request", http.StatusBadRequest}
	KeyTooLongError              = Error{"KeyTooLongError", "Your key is too long.", http.StatusBadRequest}
	MalformedXML                 = Error{"MalformedXML", "The XML you provided was not well-formed or did not validate against our published schema.", http.StatusBadRequest}
	MethodNotAllowed             = Error{"MethodNotAllowed", "The specified method is not allowed against this resource.", http.StatusMethodNotAllowed}
	MissingContentLength         = Error{"MissingContentLength", "You must provide the Content-Length HTTP header.", http.StatusLengthRequired}
	NoSuchBucket                 = Error{"NoSuchBucket", "The specified bucket does not exist.", http.StatusNotFound}
	NoSuchKey                    = Error{"NoSuchKey", "The specified key does not exist.", http.StatusNotFound}
	NoSuchUpload                 = Error{"NoSuchUpload", "The specified multipart upload does not exist.", http.StatusNotFound}
	NotImplemented               = Error{"NotImplemented", "A header or query you provided implies functionality that is not implemented.", http.StatusNotImplemented}
	PreconditionFailed           = Error{"PreconditionFailed", "At least one of the preconditions you specified did not hold.", http.StatusPreconditionFailed}
	RequestTimeTooSkewed         = Error{"RequestTimeTooSkewed", "The difference between the request time and the server's time is too large.", http.StatusForbidden}
	SignatureDoesNotMatch        = Error{"SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided.", http.StatusForbidden}
	SlowDown                     = Error{"SlowDown", "Please reduce your request rate.", http.StatusServiceUnavailable}
	XAmzContentSHA256Mismatch    = Error{"XAmzContentSHA256Mismatch", "The provided 'x-amz-content-sha256' header does not match what was computed.", http.StatusBadRequest}
)

var storageMap = []struct {
	err error
	s3  Error
}{
	{storage.ErrNoSuchBucket, NoSuchBucket},
	{storage.ErrBucketAlreadyOwnedByYou, BucketAlreadyOwnedByYou},
	{storage.ErrBucketNotEmpty, BucketNotEmpty},
	{storage.ErrInvalidBucketName, InvalidBucketName},
	{storage.ErrNoSuchKey, NoSuchKey},
	{storage.ErrInvalidKey, InvalidArgument},
	{storage.ErrKeyTooLong, KeyTooLongError},
	{storage.ErrInvalidRange, InvalidRange},
	{storage.ErrNoSuchUpload, NoSuchUpload},
	{storage.ErrInvalidPart, InvalidPart},
	{storage.ErrInvalidPartOrder, InvalidPartOrder},
	{storage.ErrEntityTooSmall, EntityTooSmall},
	{storage.ErrInvalidPartNumber, InvalidArgument},
}

// From converts any error into an S3 Error. Unknown errors become InternalError.
func From(err error) Error {
	var e Error
	if errors.As(err, &e) {
		return e
	}
	var c Coder
	if errors.As(err, &c) {
		return Error{Code: c.S3Code(), Message: err.Error(), Status: c.HTTPStatus()}
	}
	for _, m := range storageMap {
		if errors.Is(err, m.err) {
			return m.s3
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Error{Code: "RequestTimeout", Message: "Your socket connection to the server was not read from or written to within the timeout period.", Status: http.StatusBadRequest}
	}
	return InternalError
}

// WithMessage returns a copy with a custom message.
func (e Error) WithMessage(msg string) Error {
	e.Message = msg
	return e
}

type errorBody struct {
	XMLName    xml.Name `xml:"Error"`
	Code       string   `xml:"Code"`
	Message    string   `xml:"Message"`
	Resource   string   `xml:"Resource,omitempty"`
	BucketName string   `xml:"BucketName,omitempty"`
	Key        string   `xml:"Key,omitempty"`
	RequestID  string   `xml:"RequestId"`
}

// Write sends err as an S3 XML error and records the code in the request Info.
// HEAD responses carry no body, as in S3.
func Write(w http.ResponseWriter, r *http.Request, err error) {
	e := From(err)
	body := errorBody{Code: e.Code, Message: e.Message, Resource: r.URL.Path}
	if info := s3op.FromContext(r.Context()); info != nil {
		info.ErrorCode = e.Code
		body.RequestID = info.RequestID
		body.BucketName = info.Bucket
		body.Key = info.Key
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(e.Status)
		return
	}
	out, mErr := xml.Marshal(body)
	if mErr != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(e.Status)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}
