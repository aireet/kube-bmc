// Package ipmi reads and controls baseboard management controllers (BMCs) through the
// Intelligent Platform Management Interface.
//
// A Client sends commands through a Runner. Tool runs the ipmitool binary, either
// in-band on the host whose BMC is queried, which needs no network access and no
// credentials, or over the network with the lanplus interface:
//
//	local := ipmi.New(ipmi.Tool{})
//	remote := ipmi.New(ipmi.Tool{Host: "10.0.0.7", Username: "admin", Password: pw})
//
// Client methods return typed values parsed from ipmitool's output. The parsers tolerate
// the vendor differences seen in practice, such as partial output followed by an error;
// they are tested with AMI MegaRAC and Supermicro BMCs. Package ipmitest provides a
// Runner that replays recorded output, for tests without a BMC.
package ipmi
