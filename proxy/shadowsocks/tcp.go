package shadowsocks

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	sscore "github.com/shadowsocks/go-shadowsocks2/core"
	sssocks "github.com/shadowsocks/go-shadowsocks2/socks"

	"github.com/eycorsican/go-tun2socks/common/log"
	"github.com/eycorsican/go-tun2socks/core"
)

type tcpHandler struct {
	sync.Mutex

	cipher   sscore.Cipher
	server   string
	conns    map[core.TCPConn]net.Conn
	tgtAddrs map[core.TCPConn]net.Addr
	tgtSent  map[core.TCPConn]bool
}

func (h *tcpHandler) fetchInput(conn core.TCPConn, input io.Reader) {
	defer func() {
		h.Close(conn)
		conn.Close() // also close tun2socks connection here
	}()

	_, err := io.Copy(conn, input)
	if err != nil {
		// log.Printf("fetch input failed: %v", err)
		return
	}
}

func (h *tcpHandler) sendTargetAddress(conn core.TCPConn) error {
	h.Lock()
	defer h.Unlock()

	tgtAddr, ok1 := h.tgtAddrs[conn]
	rc, ok2 := h.conns[conn]
	sent, ok3 := h.tgtSent[conn]
	if ok3 && sent {
		return nil
	}
	if ok1 && ok2 {
		tgt := sssocks.ParseAddr(tgtAddr.String())
		_, err := rc.Write(tgt)
		if err != nil {
			return errors.New(fmt.Sprintf("send target address failed: %v", err))
		}
		h.tgtSent[conn] = true
		go h.fetchInput(conn, rc)
	} else {
		return errors.New("target address not found")
	}
	return nil
}

func NewTCPHandler(server, cipher, password string) core.TCPConnHandler {
	ciph, err := sscore.PickCipher(cipher, []byte{}, password)
	if err != nil {
		log.Errorf("failed to pick a cipher: %v", err)
	}

	return &tcpHandler{
		cipher:   ciph,
		server:   server,
		conns:    make(map[core.TCPConn]net.Conn, 16),
		tgtAddrs: make(map[core.TCPConn]net.Addr, 16),
		tgtSent:  make(map[core.TCPConn]bool, 16),
	}
}

func (h *tcpHandler) Connect(conn core.TCPConn, target net.Addr) error {
	rc, err := net.Dial("tcp", h.server)
	if err != nil {
		return errors.New(fmt.Sprintf("dial remote server failed: %v", err))
	}
	rc = h.cipher.StreamConn(rc)

	h.Lock()
	h.conns[conn] = rc
	h.tgtAddrs[conn] = target
	h.Unlock()
	rc.SetDeadline(time.Time{})
	log.Infof("new proxy connection for target: %s:%s", target.Network(), target.String())
	return nil
}

func (h *tcpHandler) DidReceive(conn core.TCPConn, data []byte) error {
	h.Lock()
	rc, ok1 := h.conns[conn]
	h.Unlock()

	if ok1 {
		err := h.sendTargetAddress(conn)
		if err != nil {
			h.Close(conn)
			return err
		}
		_, err = rc.Write(data)
		if err != nil {
			h.Close(conn)
			return errors.New(fmt.Sprintf("write remote failed: %v", err))
		}
		return nil
	} else {
		h.Close(conn)
		return errors.New(fmt.Sprintf("proxy connection %v->%v does not exists", conn.LocalAddr(), conn.RemoteAddr()))
	}
}

func (h *tcpHandler) DidClose(conn core.TCPConn) {
	h.Close(conn)
}

func (h *tcpHandler) LocalDidClose(conn core.TCPConn) {
	h.Close(conn)
}

func (h *tcpHandler) Close(conn core.TCPConn) {
	h.Lock()
	defer h.Unlock()

	if rc, found := h.conns[conn]; found {
		rc.Close()
	}

	delete(h.conns, conn)
	delete(h.tgtAddrs, conn)
	delete(h.tgtSent, conn)
}
