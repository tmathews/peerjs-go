package peer

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/muka/peerjs-go/emitter"
	"github.com/muka/peerjs-go/enums"
	"github.com/muka/peerjs-go/models"
	"github.com/sirupsen/logrus"
)

// SocketEvent carries an event from the socket
type SocketEvent struct {
	Type    string
	Message *models.Message
	Error   error
}

// NewSocket create a socket instance
func NewSocket(opts Options) *Socket {
	s := &Socket{
		Emitter: emitter.NewEmitter(),
		log:     createLogger("socket", opts.Debug),
	}
	s.opts = opts
	return s
}

// Socket abstract websocket exposing an event emitter like interface
type Socket struct {
	emitter.Emitter
	id          string
	opts        Options
	baseURL     string
	conn        *websocket.Conn
	log         *logrus.Entry
	mutex       sync.Mutex
	wsPingTimer *time.Timer
}

func (s *Socket) buildBaseURL() string {
	proto := "ws"
	if s.opts.Secure {
		proto = "wss"
	}
	port := strconv.Itoa(s.opts.Port)

	path := s.opts.Path
	if path == "/" {
		path = ""
	}

	return fmt.Sprintf(
		"%s://%s:%s%s/peerjs?key=%s",
		proto,
		s.opts.Host,
		port,
		path,
		s.opts.Key,
	)
}

func (s *Socket) scheduleHeartbeat() {
	s.wsPingTimer = time.AfterFunc(time.Millisecond*time.Duration(s.opts.PingInterval), func() {
		s.sendHeartbeat()
	})
}

func (s *Socket) sendHeartbeat() {
	if s.conn == nil {
		s.log.Debug(`Cannot send heartbeat, because socket closed`)
		return
	}

	msg := models.Message{
		Type: enums.ServerMessageTypeHeartbeat,
	}

	res, err := json.Marshal(msg)
	if err != nil {
		s.log.Errorf("sendHeartbeat: Failed to serialize message: %s", err)
	}

	// s.log.Debug("Send heartbeat")
	err = s.Send(res)
	if err != nil {
		s.log.Errorf("sendHeartbeat: Failed to send message: %s", err)
		return
	}

	s.scheduleHeartbeat()
}

// Start initiate the connection
func (s *Socket) Start(id string, token string) error {

	if s.conn != nil {
		return nil
	}

	if s.baseURL == "" {
		s.baseURL = s.buildBaseURL()
	}

	url := s.baseURL + fmt.Sprintf("&id=%s&token=%s", id, token)
	s.log.Debugf("Connecting to %s", url)
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return err
	}
	s.conn = c

	s.conn.SetCloseHandler(func(code int, text string) error {
		s.log.Debug("WS closed")
		s.conn = nil
		return nil
	})

	// ws ping by sending heartbeat message
	s.scheduleHeartbeat()

	// collect messages
	go func() {
		for {

			if s.conn == nil {
				s.log.Debug("WS connection unset, closing read go routine")
				return
			}

			msgType, raw, err := s.conn.ReadMessage()
			s.log.Debugf("WS msg %v", msgType)
			if err != nil {
				// catch close error, avoid panic reading a closed conn
				if _, ok := err.(*websocket.CloseError); ok {
					s.log.Debugf("websocket closed: %s", err)
					s.Emit(enums.SocketEventTypeDisconnected, SocketEvent{enums.SocketEventTypeDisconnected, nil, err})
					return
				} else if opErr, ok := err.(*net.OpError); ok {
					s.log.Debugf("websocket closed: %s OpErr Op %s", opErr, opErr.Op)
					s.Emit(enums.SocketEventTypeDisconnected, SocketEvent{enums.SocketEventTypeDisconnected, nil, err})
					return
				}
				s.log.Warnf("websocket read error: %s", err)
				continue
			}

			s.log.Infof("websocket message: %s", raw)

			if msgType == websocket.TextMessage {

				msg := models.Message{}
				err = json.Unmarshal(raw, &msg)
				if err != nil {
					s.log.Errorf("Failed to decode websocket message=%s %s", string(raw), err)
				}

				s.Emit(enums.SocketEventTypeMessage, SocketEvent{enums.SocketEventTypeMessage, &msg, err})
			} else {
				s.log.Warnf("Unmanaged websocket message type %d", msgType)
			}

		}
	}()

	return nil
}

// Close close the websocket connection
func (s *Socket) Close() error {
	if s.conn == nil {
		return nil
	}
	err := s.conn.WriteMessage(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
	)
	if err != nil {
		s.log.Debugf("Failed to send close message: %s", err)
	}
	err = s.conn.Close()
	if err != nil {
		s.log.Warnf("WS close error: %s", err)
	}
	s.log.Debug("Closed websocket")
	s.conn = nil
	return err
}

// Send send a message
func (s *Socket) Send(msg []byte) error {
	if s.conn == nil {
		return nil
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.conn.WriteMessage(websocket.TextMessage, msg)
}
