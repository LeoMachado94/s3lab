package storage

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateBucketName(t *testing.T) {
	valid := []string{"abc", "my-bucket", "my.bucket.1", "a1b2c3"}
	invalid := []string{"ab", "Upper", "-abc", "abc-", "a..b", "a.-b", "192.168.0.1", "under_score", string(make([]byte, 64))}
	for _, n := range valid {
		assert.NoError(t, ValidateBucketName(n), n)
	}
	for _, n := range invalid {
		assert.ErrorIs(t, ValidateBucketName(n), ErrInvalidBucketName, n)
	}
}

func TestValidateKey(t *testing.T) {
	assert.NoError(t, ValidateKey("../a//b"))
	assert.ErrorIs(t, ValidateKey(""), ErrInvalidKey)
	assert.ErrorIs(t, ValidateKey("\xff"), ErrInvalidKey)
	long := make([]byte, MaxKeyBytes+1)
	for i := range long {
		long[i] = 'a'
	}
	assert.ErrorIs(t, ValidateKey(string(long)), ErrKeyTooLong)
}

func TestByteRangeResolve(t *testing.T) {
	cases := []struct {
		r           ByteRange
		size        int64
		off, length int64
		err         bool
	}{
		{ByteRange{0, 9}, 100, 0, 10, false},
		{ByteRange{90, -1}, 100, 90, 10, false},
		{ByteRange{90, 500}, 100, 90, 10, false},
		{ByteRange{-5, -1}, 100, 95, 5, false},
		{ByteRange{-500, -1}, 100, 0, 100, false},
		{ByteRange{100, -1}, 100, 0, 0, true},
		{ByteRange{5, 4}, 100, 0, 0, true},
		{ByteRange{-1, -1}, 0, 0, 0, true},
	}
	for _, c := range cases {
		off, n, err := c.r.Resolve(c.size)
		if c.err {
			assert.ErrorIs(t, err, ErrInvalidRange, "%+v", c.r)
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, c.off, off)
		assert.Equal(t, c.length, n)
	}
}

func TestMultipartETag(t *testing.T) {
	// Known value: two parts with md5("a") and md5("b").
	etag, err := MultipartETag([]string{"0cc175b9c0f1b6a831c399e269772661", `"92eb5ffee6ae2fec3ad71c777531578f"`})
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{32}-2$`, etag)
	_, err = MultipartETag([]string{"zz"})
	assert.ErrorIs(t, err, ErrInvalidPart)
}

func TestValidateCompleteParts(t *testing.T) {
	up := map[int]PartInfo{
		1: {PartNumber: 1, ETag: "a", Size: MinPartSize},
		2: {PartNumber: 2, ETag: "b", Size: 1},
		3: {PartNumber: 3, ETag: "c", Size: 1},
	}
	assert.NoError(t, ValidateCompleteParts([]CompletePart{{1, `"a"`}, {2, "b"}}, up))
	assert.ErrorIs(t, ValidateCompleteParts(nil, up), ErrInvalidPart)
	assert.ErrorIs(t, ValidateCompleteParts([]CompletePart{{2, "b"}, {1, "a"}}, up), ErrInvalidPartOrder)
	assert.ErrorIs(t, ValidateCompleteParts([]CompletePart{{1, "x"}}, up), ErrInvalidPart)
	assert.ErrorIs(t, ValidateCompleteParts([]CompletePart{{2, "b"}, {3, "c"}}, up), ErrEntityTooSmall)
}

func TestListFromSorted(t *testing.T) {
	keys := []string{"a.txt", "dir/a", "dir/b", "dir/sub/c", "other/x", "z"}
	objs := make([]ObjectInfo, len(keys))
	for i, k := range keys {
		objs[i] = ObjectInfo{Key: k}
	}
	names := func(r ListResult) []string {
		var out []string
		for _, o := range r.Objects {
			out = append(out, o.Key)
		}
		return out
	}

	r := ListFromSorted(objs, ListOptions{Delimiter: "/"})
	assert.Equal(t, []string{"a.txt", "z"}, names(r))
	assert.Equal(t, []string{"dir/", "other/"}, r.CommonPrefixes)
	assert.False(t, r.IsTruncated)

	r = ListFromSorted(objs, ListOptions{Prefix: "dir/", Delimiter: "/"})
	assert.Equal(t, []string{"dir/a", "dir/b"}, names(r))
	assert.Equal(t, []string{"dir/sub/"}, r.CommonPrefixes)

	// Paginate with delimiter, one entry per page, collecting everything.
	var got []string
	after := ""
	for i := 0; i < 10; i++ {
		r = ListFromSorted(objs, ListOptions{Delimiter: "/", MaxKeys: 1, StartAfter: after})
		got = append(got, names(r)...)
		got = append(got, r.CommonPrefixes...)
		if !r.IsTruncated {
			break
		}
		after = r.NextStartAfter
	}
	assert.Equal(t, []string{"a.txt", "dir/", "other/", "z"}, got)

	r = ListFromSorted(objs, ListOptions{Prefix: "nope"})
	assert.Empty(t, r.Objects)
	assert.Equal(t, fmt.Sprint([]string(nil)), fmt.Sprint(r.CommonPrefixes))
}
