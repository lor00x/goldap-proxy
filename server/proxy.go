package server

import (
	//	"bufio"
	"errors"
	"fmt"
	"github.com/kr/pretty"
	"github.com/lor00x/goldap/message"
	"log"
	"net"
)

// The Proxy is a program-in-the-middle which will dump every LDAP structures
// exchanged between the client and the server
type Proxy struct {
	name       string
	dumpChan   chan Message
	clientConn net.Conn
	serverConn net.Conn
	clientChan chan Message
	serverChan chan Message
}

// To dump each request we have to read the ASN.1 first bytes to get the lengh of the message
// then build a slice of bytes with the correct quantity of data
type Message struct {
	id     int
	source string
	bytes  []byte
}

func Forward(local string, remote string) {
	listener, err := net.Listen("tcp", local)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Listening on port %s...", local)

	i := 0
	for {
		i++
		var err error
		proxy := Proxy{name: fmt.Sprintf("PROXY%d", i)}
		proxy.clientConn, err = listener.Accept()
		if err != nil {
			log.Println(err)
			continue
		}
		proxy.serverConn, err = net.Dial("tcp", remote)
		if err != nil {
			log.Println(err)
			continue
		}

		log.Printf("New connection accepted")
		go proxy.start()
	}
}

func (p *Proxy) start() {
	p.dumpChan = make(chan Message)
	p.clientChan = make(chan Message)
	p.serverChan = make(chan Message)
	go p.dump()
	go p.readClient()
	go p.writeServer()
	go p.readServer()
	go p.writeClient()
}

func (p *Proxy) readClient() {
	messageid := 1
	for {
		var err error
		var bytes *[]byte
		bytes, err = p.readLdapMessageBytes(p.clientConn)
		if err != nil {
			p.clientConn.Close()
			log.Printf("%s: %s", p.name, "CLIENT DISCONNECTED")
			break
		}

		messageid++
		message := Message{id: messageid, source: "CLIENT", bytes: *bytes}
		p.dumpChan <- message
		p.serverChan <- message
	}
}

func (p *Proxy) writeServer() {
	for msg := range p.serverChan {
		p.serverConn.Write(msg.bytes)
	}
}

func (p *Proxy) readServer() {
	messageid := 0
	for {
		var err error
		var bytes *[]byte
		bytes, err = p.readLdapMessageBytes(p.serverConn)
		if err != nil {
			p.serverConn.Close()
			log.Printf("%s: %s", p.name, "SERVER DISCONNECTED")
			break
		}
		messageid++
		message := Message{id: messageid, source: "SERVER", bytes: *bytes}
		p.dumpChan <- message
		p.clientChan <- message
	}
}

func (p *Proxy) writeClient() {
	for msg := range p.clientChan {
		p.clientConn.Write(msg.bytes)
	}
}

func (p *Proxy) dump() {
	for msg := range p.dumpChan {
		result := ""
		//		for _, onebyte := range msg.bytes {
		//			if onebyte < 0x10 {
		//				result = fmt.Sprintf("%s, 0x0%x", result, onebyte)
		//			} else {
		//				result = fmt.Sprintf("%s, 0x%x", result, onebyte)
		//			}
		//		}
		// Now decode the message
		message, err := p.decodeMessage(msg.bytes)
		if err != nil {
			result = fmt.Sprintf("%s\n%s", result, err.Error())
		} else {
			result = fmt.Sprintf("%s\n%# v", result, pretty.Formatter(message))
		}
		log.Printf("Message: %s - %s - msg %d %s\n\n", p.name, msg.source, msg.id, result)
	}
}

//func (p *Proxy) dumpCols() {
//	for msg := range p.dumpChan {
//		result := ""
////		for _, onebyte := range msg.bytes {
////			if onebyte < 0x10 {
////				result = fmt.Sprintf("%s, 0x0%x", result, onebyte)
////			} else {
////				result = fmt.Sprintf("%s, 0x%x", result, onebyte)
////			}
////		}
//		// Now decode the message
//		message, err := p.decodeMessage(msg.bytes)
//		if err != nil {
//			result = fmt.Sprintf("%s\n%s", result, err.Error())
//		} else {
//			result = fmt.Sprintf("%s\n%# v", result, pretty.Formatter(message))
//		}
//		log.Printf("%s - %s - msg %d %s", p.name, msg.source, msg.id, result)
//	}
//}

func (p *Proxy) decodeMessage(bytes []byte) (ret message.LDAPMessage, err error) {
	defer func() {
		if e := recover(); e != nil {
			err = errors.New(fmt.Sprintf("%s", e))
		}
	}()
	zero := 0
	ret, err = message.ReadLDAPMessage(message.NewBytes(zero, bytes))
	return
}

func (p *Proxy) readLdapMessageBytes(conn net.Conn) (ret *[]byte, err error) {
	var bytes []byte
	var tagAndLength message.TagAndLength
	tagAndLength, err = p.readTagAndLength(conn, &bytes)
	if err != nil {
		return
	}
	p.readBytes(conn, &bytes, tagAndLength.Length)
	return &bytes, err
}

// Read "length" bytes from the connection
// Append the read bytes to "bytes"
// Return the last read byte
func (p *Proxy) readBytes(conn net.Conn, bytes *[]byte, length int) (b byte, err error) {
	newbytes := make([]byte, length)
	n, err := conn.Read(newbytes)
	if n != length {
		fmt.Errorf("%d bytes read instead of %d", n, length)
	} else if err != nil {
		return
	}
	*bytes = append(*bytes, newbytes...)
	b = (*bytes)[len(*bytes)-1]
	return
}

// readTagAndLength parses an ASN.1 tag and length pair from a live connection
// into a byte slice. It returns the parsed data and the new offset. SET and
// SET OF (tag 17) are mapped to SEQUENCE and SEQUENCE OF (tag 16) since we
// don't distinguish between ordered and unordered objects in this code.
func (p *Proxy) readTagAndLength(conn net.Conn, bytes *[]byte) (ret message.TagAndLength, err error) {
	// offset = initOffset
	//b := bytes[offset]
	//offset++
	var b byte
	b, err = p.readBytes(conn, bytes, 1)
	if err != nil {
		return
	}
	ret.Class = int(b >> 6)
	ret.IsCompound = b&0x20 == 0x20
	ret.Tag = int(b & 0x1f)

	//	// If the bottom five bits are set, then the tag number is actually base 128
	//	// encoded afterwards
	//	if ret.tag == 0x1f {
	//		ret.tag, err = parseBase128Int(conn, bytes)
	//		if err != nil {
	//			return
	//		}
	//	}
	// We are expecting the LDAP sequence tag 0x30 as first byte
	if b != 0x30 {
		panic(fmt.Sprintf("Expecting 0x30 as first byte, but got %#x instead", b))
	}

	b, err = p.readBytes(conn, bytes, 1)
	if err != nil {
		return
	}
	if b&0x80 == 0 {
		// The length is encoded in the bottom 7 bits.
		ret.Length = int(b & 0x7f)
	} else {
		// Bottom 7 bits give the number of length bytes to follow.
		numBytes := int(b & 0x7f)
		if numBytes == 0 {
			err = message.SyntaxError{"indefinite length found (not DER)"}
			return
		}
		ret.Length = 0
		for i := 0; i < numBytes; i++ {

			b, err = p.readBytes(conn, bytes, 1)
			if err != nil {
				return
			}
			if ret.Length >= 1<<23 {
				// We can't shift ret.length up without
				// overflowing.
				err = message.StructuralError{"length too large"}
				return
			}
			ret.Length <<= 8
			ret.Length |= int(b)
			if ret.Length == 0 {
				// DER requires that lengths be minimal.
				err = message.StructuralError{"superfluous leading zeros in length"}
				return
			}
		}
	}

	return
}
