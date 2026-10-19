package storage

import "errors"

// Sentinel errors returned by backends. s3api maps them to S3 error codes;
// wrap with fmt.Errorf("...: %w", err) to add context.
var (
	ErrNoSuchBucket            = errors.New("no such bucket")
	ErrBucketAlreadyOwnedByYou = errors.New("bucket already exists")
	ErrBucketNotEmpty          = errors.New("bucket not empty")
	ErrInvalidBucketName       = errors.New("invalid bucket name")
	ErrNoSuchKey               = errors.New("no such key")
	ErrInvalidKey              = errors.New("invalid object key")
	ErrKeyTooLong              = errors.New("object key too long")
	ErrInvalidRange            = errors.New("requested range not satisfiable")
	ErrNoSuchUpload            = errors.New("no such upload")
	ErrInvalidPart             = errors.New("invalid part")
	ErrInvalidPartOrder        = errors.New("invalid part order")
	ErrEntityTooSmall          = errors.New("part smaller than minimum allowed size")
	ErrInvalidPartNumber       = errors.New("part number must be between 1 and 10000")
)
