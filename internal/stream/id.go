package stream

import (
	"errors"
	"strconv"
)

// Mirakurun service ids are networkId*100000 + serviceId.
const idModulus = 100000

// ErrInvalidID is returned for a malformed or out-of-range service id.
var ErrInvalidID = errors.New("invalid service id")

// ParseID validates the {id} of a request path. It returns the Mirakurun
// service id and the 16-bit serviceId that tsreadex's -n option takes.
// A serviceId of 0 is rejected: "-n 0" would disable tsreadex's service
// filter and silently turn the other options off.
func ParseID(s string) (id uint64, serviceID int, err error) {
	if len(s) == 0 || len(s) > 10 {
		return 0, 0, ErrInvalidID
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, 0, ErrInvalidID
		}
	}
	id, err = strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, 0, ErrInvalidID
	}
	serviceID = int(id % idModulus)
	if serviceID < 1 || serviceID > 65535 {
		return 0, 0, ErrInvalidID
	}
	return id, serviceID, nil
}
