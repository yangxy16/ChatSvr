package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type msgBuf struct {
	buf []byte
}

/*tcp客户端结构体*/
type tcpClient struct {
	userFlag    string
	userConn    net.Conn
	handShaked  bool
	sendMsgChan chan msgBuf
	closeOnce   sync.Once
}

/*用户消息*/
type msgInfo struct {
	msgHead uint32
	msgLen  uint32
	MsgBody []byte
}

/*WebSocket消息类型*/
const (
	ContinuationFrame = 0x0
	TextFrame         = 0x1
	BinaryFrame       = 0x2
	ConnectionClose   = 0x8
	Ping              = 0x9
	Pong              = 0xA
)

/*WebSocket消息帧*/
type wsFrame struct {
	_fin        uint8
	_opcode     uint8
	_mask       uint8
	_payloadlen uint64
	_maskKey    []byte
	_msg        []byte
}

type msgData struct {
	MsgType   string `json:"msgType"`
	MsgRemote string `json:"msgRemote"`
	MsgBody   string `json:"msgBody"`
}

type msgOffline struct {
	id  string
	buf []byte
}

const (
	MESSAGE_ADMIN          = "10000"
	MESSAGE_LOGIN          = "LOGIN"
	MESSAGE_TRANS2USER     = "TRANS2USER"
	MESSAGE_LOGIN_OK       = "OK"
	MESSAGE_LOGIN_FAIL     = "RECONNECT"
	MESSAGE_HEART_BEAT     = "HEARTBEAT"
	MESSAGE_REMOTE_OFFLINE = "REMOTEOFFLINE"
)

const OFFLINEFILEPATH = "./OfflineMessage/"
const TIMEBRAODCAST = 120
const MAXCONN = 20000

var clientJoinChannel chan net.Conn         //等待建立链接的通道
var clientIDList map[string]*tcpClient      //客户端列表
var clientListLock sync.Mutex               //客户端列表读写锁
var clientOfflineMsgChannel chan msgOffline //客户端离线消息写入列表
var clientMaxCount int64                    //客户端最大连接数量
var clientCurrentCount int64                //当前客户端连接数量

func addLog(msg ...interface{}) {
	fmt.Print(time.Now().Format("2006-01-02 15:04:05 "))
	fmt.Println(msg...)
}

/*客户端握手协程*/
func (this *tcpClient) handShake() {
	bHttpHeader := make([]byte, 1024)
	nRecvLen := 0

	defer func() {
		bHttpHeader = nil
		if !this.handShaked {
			this.closeClient()
		}
	}()

	for {
		if n, err := this.userConn.Read(bHttpHeader[nRecvLen:]); err != nil {
			return
		} else {
			nRecvLen += n
			strRecvCache := string(bHttpHeader)
			if !strings.Contains(strRecvCache, "GET") {
				return
			}
			if nHttpEndPos := strings.Index(strRecvCache, "\r\n\r\n"); nHttpEndPos != -1 {
				if nStartPos := strings.Index(strRecvCache, "Sec-WebSocket-Key: "); nStartPos != -1 {
					strRecvCache = string([]byte(strRecvCache)[nStartPos:])
					if nEndPos := strings.Index(strRecvCache, "\r\n"); nEndPos != -1 {
						strKey := string([]byte(strRecvCache)[19:nEndPos])
						sha1Encoder := sha1.New()
						sha1Encoder.Write([]byte(strKey + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
						szSendBackBuf := fmt.Sprintf("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nServer: JackYang WebSocket Server 1.0\r\nUpgrade: WebSocket\r\nAccess-Control-Allow-Credentials: true\r\nAccess-Control-Allow-Headers: content-type\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sha1Encoder.Sum(nil)))
						sha1Encoder = nil
						if _, err := this.userConn.Write([]byte(szSendBackBuf)); err != nil {
							return
						} else {
							this.handShaked = true
							break
						}
					} else {
						break
					}
				} else {
					break
				}
			}
		}
	}

	if this.handShaked {
		go this.readLoop()
		go this.writeLoop()
	}
}

/*客户端消息接收协程*/
func (this *tcpClient) readLoop() (err error) {
	defer func() {
		recover()
		this.closeClient()
	}()

	for {
		bTotalBuf := make([]byte, 0)
		bMsgBuf := make([]byte, 1)

		defer func() {
			bTotalBuf = nil
			bMsgBuf = nil
		}()
		for {
			var frame wsFrame
			if _, err := this.userConn.Read(bMsgBuf); err != nil {
				return err
			}

			nTempLen := uint8(bMsgBuf[0])
			frame._fin = nTempLen >> 7
			frame._opcode = nTempLen & 0x0f

			if frame._opcode == ConnectionClose {
				this.sendCloseMsg()
				return nil
			} else if frame._opcode == Ping {
				this.sendPongMsg()
				break
			}

			if _, err := this.userConn.Read(bMsgBuf); err != nil {
				return err
			}

			nTempLen = uint8(bMsgBuf[0])
			frame._mask = nTempLen >> 7
			frame._payloadlen = uint64(nTempLen & 0x7f)

			if frame._payloadlen <= 125 {
				//Nothing
			} else if frame._payloadlen == 126 {
				bMsgBuf := make([]byte, 2)
				if _, err := this.userConn.Read(bMsgBuf); err != nil {
					return err
				}
				frame._payloadlen = uint64(binary.BigEndian.Uint16(bMsgBuf))
			} else if frame._payloadlen == 127 {
				bMsgBuf := make([]byte, 8)
				if _, err := this.userConn.Read(bMsgBuf); err != nil {
					return err
				}
				frame._payloadlen = binary.BigEndian.Uint64(bMsgBuf)
			} else {
				this.sendCloseMsg()
				return nil
			}

			if frame._mask == 1 {
				frame._maskKey = make([]byte, 4)
				if _, err := this.userConn.Read(frame._maskKey); err != nil {
					return err
				}
			}

			if frame._payloadlen > 0 {
				frame._msg = make([]byte, frame._payloadlen)
				nRecvLen := 0
				for {
					if recvLen, err := this.userConn.Read(frame._msg[nRecvLen:]); err != nil {
						return err
					} else {
						nRecvLen += recvLen
					}
					if uint64(nRecvLen) >= frame._payloadlen {
						break
					}
				}

				if frame._mask == 1 {
					for i := uint64(0); i < frame._payloadlen; i++ {
						frame._msg[i] = frame._msg[i] ^ frame._maskKey[i%4]
					}
				}

				bTotalBuf = append(bTotalBuf, frame._msg...)
				frame._maskKey = nil
				frame._msg = nil

				if frame._fin == 1 {
					ret := this.readMsg(frame._opcode, bTotalBuf)
					bTotalBuf = nil
					if !ret {
						return nil
					}
					break
				}
			}
		}
		this.userConn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		runtime.Gosched()
	}
	return nil
}

/*客户端消息发送协程*/
func (this *tcpClient) writeLoop() {
	defer func() {
		recover()
		this.closeClient()
	}()

	for msg := range this.sendMsgChan {
		if _, err := this.userConn.Write(msg.buf); err != nil {
			_opcode := uint8(msg.buf[0]) & 0x0f
			if _opcode == TextFrame { //只记录文本类型的消息
				addOfflineMsg(this.userFlag, msg.buf)
			}
			break
		}
		runtime.Gosched()
	}
}

/*处理客户端消息发送*/
func (this *tcpClient) sendTextMsg(msg []byte) {
	var buffer msgBuf
	buffer.buf = this.makeWsFrame(TextFrame, msg)
	this.sendMsgChan <- buffer
}

/*处理客户端消息发送*/
func (this *tcpClient) sendPongMsg() {
	var buffer msgBuf
	buffer.buf = this.makeWsFrame(Pong, []byte(""))
	this.sendMsgChan <- buffer
}

/*处理客户端消息发送*/
func (this *tcpClient) sendCloseMsg() {
	var buffer msgBuf
	buffer.buf = this.makeWsFrame(ConnectionClose, []byte(""))
	this.sendMsgChan <- buffer
}

/*处理客户端消息发送*/
func (this *tcpClient) sendBinaryMsg(msg []byte) {
	var buffer msgBuf
	buffer.buf = this.makeWsFrame(BinaryFrame, msg)
	this.sendMsgChan <- buffer
}

/*处理客户端消息接收*/
func (this *tcpClient) readMsg(opcode uint8, buf []byte) (ret bool) {
	defer func() {
		recover()
	}()

	switch opcode {
	case TextFrame:
		msg := string(buf)
		var msgJson msgData
		err := json.Unmarshal([]byte(msg), &msgJson)
		if err != nil {
			return false
		}
		if len(this.userFlag) < 1 {
			if msgJson.MsgType != MESSAGE_LOGIN || msgJson.MsgRemote != MESSAGE_ADMIN {
				return false
			}
			this.userFlag = msgJson.MsgBody
			clientListLock.Lock()
			_, ok := clientIDList[this.userFlag]
			if !ok {
				clientIDList[this.userFlag] = this
			}
			clientListLock.Unlock()
			if ok {
				msgSend := msgData{MESSAGE_LOGIN, MESSAGE_ADMIN, MESSAGE_LOGIN_FAIL}
				msgSendBuf, _ := json.Marshal(msgSend)
				this.sendTextMsg(msgSendBuf)
				return false
			}
			msgSend := msgData{MESSAGE_LOGIN, MESSAGE_ADMIN, MESSAGE_LOGIN_OK}
			msgSendBuf, _ := json.Marshal(msgSend)
			this.sendTextMsg(msgSendBuf)
			msgOfflineData, ok := this.readOfflineMsg()
			if ok {
				for i := 0; i < len(msgOfflineData); i++ {
					this.sendTextMsg(msgOfflineData[i].buf)
				}
			}
		} else {
			if msgJson.MsgType == MESSAGE_TRANS2USER && len(msgJson.MsgRemote) > 0 {
				msgSend := msgData{MESSAGE_TRANS2USER, this.userFlag, msgJson.MsgBody}
				msgSendBuf, _ := json.Marshal(msgSend)
				clientListLock.Lock()
				dstUser, ok := clientIDList[msgJson.MsgRemote]
				if ok {
					dstUser.sendTextMsg(msgSendBuf)
				} else {
					addOfflineMsg(msgJson.MsgRemote, msgSendBuf)
					msgSend := msgData{MESSAGE_REMOTE_OFFLINE, msgJson.MsgRemote, ""}
					msgSendBuf, _ := json.Marshal(msgSend)
					this.sendTextMsg(msgSendBuf)
				}
				clientListLock.Unlock()
				msgSendBuf = nil
			} else if msgJson.MsgType == MESSAGE_HEART_BEAT {
				msgSend := msgData{MESSAGE_HEART_BEAT, MESSAGE_ADMIN, ""}
				msgSendBuf, _ := json.Marshal(msgSend)
				this.sendTextMsg(msgSendBuf)
			}
		}
		break
	case BinaryFrame:
		return false
	}
	return true
}

/*处理客户端关闭*/
func (this *tcpClient) closeClient() {
	this.closeOnce.Do(func() {
		close(this.sendMsgChan)
		this.userConn.Close()
		atomic.AddInt64(&clientCurrentCount, -1)
		if len(this.userFlag) > 0 {
			clientListLock.Lock()
			delete(clientIDList, this.userFlag)
			clientListLock.Unlock()
		}
	})
}

/*处理Websocket消息帧*/
func (this *tcpClient) makeWsFrame(opcode uint8, lpBuffer []byte) (ret []byte) {
	defer func() {
		recover()
	}()

	var fin uint8 = 1
	var mask uint8 = 0
	var c1 uint8 = 0x00
	var c2 uint8 = 0x00
	c1 = c1 | (fin << 7)
	c1 = c1 | opcode
	c2 = c2 | (mask << 7)

	nBufLen := len(lpBuffer)
	pSendBuffer := bytes.NewBuffer(nil)

	if nBufLen == 0 {
		binary.Write(pSendBuffer, binary.BigEndian, c1)
		binary.Write(pSendBuffer, binary.BigEndian, c2)
	} else if nBufLen > 0 && nBufLen <= 125 {
		binary.Write(pSendBuffer, binary.BigEndian, c1)
		binary.Write(pSendBuffer, binary.BigEndian, c2+uint8(nBufLen))
		binary.Write(pSendBuffer, binary.BigEndian, lpBuffer)
	} else if nBufLen >= 126 && nBufLen <= 65535 {
		binary.Write(pSendBuffer, binary.BigEndian, c1)
		binary.Write(pSendBuffer, binary.BigEndian, c2+126)
		tmplen := uint16(nBufLen)
		binary.Write(pSendBuffer, binary.BigEndian, tmplen)
		binary.Write(pSendBuffer, binary.BigEndian, lpBuffer)
	} else if nBufLen >= 65536 {
		binary.Write(pSendBuffer, binary.BigEndian, c1)
		binary.Write(pSendBuffer, binary.BigEndian, c2+127)
		tmplen := uint64(nBufLen)
		binary.Write(pSendBuffer, binary.BigEndian, tmplen)
		binary.Write(pSendBuffer, binary.BigEndian, lpBuffer)
	}
	return pSendBuffer.Bytes()
}

func PathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

/*读取客户端离线消息*/
func (this *tcpClient) readOfflineMsg() ([]msgBuf, bool) {
	defer func() {
		recover()
	}()

	msgOfflineData := make([]msgBuf, 0)
	msgFile := OFFLINEFILEPATH + this.userFlag + ".txt"
	if exits, _ := PathExists(msgFile); exits != true {
		return msgOfflineData, false
	}
	fRead, err := os.OpenFile(msgFile, os.O_RDONLY, 0)
	defer fRead.Close()

	if err != nil {
		return msgOfflineData, false
	}
	bufLen := make([]byte, 4)

	for {
		var msg msgBuf
		_, err := fRead.Read(bufLen)
		if err != nil {
			if err != io.EOF {
				return msgOfflineData, false
			} else {
				break
			}
		}
		dataLen := binary.LittleEndian.Uint32(bufLen)
		msg.buf = make([]byte, dataLen)
		_, errBuf := fRead.Read(msg.buf)
		if errBuf != nil {
			if errBuf != io.EOF {
				return msgOfflineData, false
			} else {
				break
			}
		}
		msgOfflineData = append(msgOfflineData, msg)
	}
	fRead.Close()
	for i := 1; i <= 3; i++ {
		err := os.Remove(msgFile)
		if err == nil {
			break
		}
	}
	return msgOfflineData, true
}

/*处理客户端连接*/
func clientConnHandler(joinChan chan net.Conn) {

	defer recover()
	for conn := range joinChan {
		if clientCurrentCount < clientMaxCount {
			atomic.AddInt64(&clientCurrentCount, 1)
			c := &tcpClient{userFlag: "", userConn: conn, handShaked: false, sendMsgChan: make(chan msgBuf, 32)}
			conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
			go c.handShake()
		} else {
			conn.Close()
		}
	}
}

func addOfflineMsg(id string, msg []byte) {
	clientOfflineMsgChannel <- msgOffline{id, msg}
}

func clientOfflineMsgHandler(msgChan chan msgOffline) {
	for offlineMsg := range msgChan {
		f, err := os.OpenFile(OFFLINEFILEPATH+offlineMsg.id+".txt", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
		if err == nil {
			buf := bytes.NewBuffer(nil)
			binary.Write(buf, binary.LittleEndian, uint32(len(offlineMsg.buf)))
			binary.Write(buf, binary.LittleEndian, offlineMsg.buf)
			f.Write(buf.Bytes())
		}
		f.Close()
		runtime.Gosched()
	}
}

func clientCountBroadCast() {
	for {
		time.Sleep(TIMEBRAODCAST * time.Second)
		addLog("当前客户端数量：", clientCurrentCount)
	}
}

func main() {

	if exits, _ := PathExists(OFFLINEFILEPATH); exits != true {
		os.MkdirAll(OFFLINEFILEPATH, os.ModePerm)
	}

	ipaddr := flag.String("ipaddr", "0.0.0.0:8000", "tcp service listen address")
	maxconn := flag.Uint64("maxconn", MAXCONN, "tcp service max client count")

	runtime.GOMAXPROCS(runtime.NumCPU())

	clientJoinChannel = make(chan net.Conn, 256)
	clientOfflineMsgChannel = make(chan msgOffline, 1024)
	go clientConnHandler(clientJoinChannel)
	go clientOfflineMsgHandler(clientOfflineMsgChannel)
	clientIDList = make(map[string]*tcpClient)

	flag.Parse()

	clientMaxCount = int64(*maxconn)
	if clientMaxCount <= 0 || clientMaxCount >= MAXCONN {
		clientMaxCount = MAXCONN
	}

	svrSock, err := net.Listen("tcp", *ipaddr)
	if err != nil {
		addLog("地址被占用，服务无法启动")
		return
	}
	defer func() {
		svrSock.Close()
	}()

	addLog("服务启动成功")
	go clientCountBroadCast()

	for {
		conn, err := svrSock.Accept()
		if err != nil {
			continue
		}
		clientJoinChannel <- conn
	}
}
