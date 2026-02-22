package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// --- Config ---

type haConfig struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

var haCfg *haConfig

func getHAConfig() (*haConfig, error) {
	if haCfg != nil {
		return haCfg, nil
	}
	data, err := os.ReadFile("homeassistant.json")
	if err != nil {
		return nil, fmt.Errorf("cannot read homeassistant.json: %w", err)
	}
	var cfg haConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid homeassistant.json: %w", err)
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	haCfg = &cfg
	return haCfg, nil
}

// --- WebSocket message ---

type wsMsg struct {
	ID      int             `json:"id"`
	Type    string          `json:"type"`
	Success *bool           `json:"success"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// --- Registry types ---

type haAreaEntry struct {
	AreaID  string   `json:"area_id"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	FloorID *string  `json:"floor_id"`
}

type haFloorEntry struct {
	FloorID string `json:"floor_id"`
	Name    string `json:"name"`
	Level   int    `json:"level"`
}

type haEntityReg struct {
	EntityID     string      `json:"entity_id"`
	AreaID       *string     `json:"area_id"`
	DeviceID     *string     `json:"device_id"`
	Name         *string     `json:"name"`
	OriginalName interface{} `json:"original_name"` // string, number, or null
	Aliases      []string    `json:"aliases"`
	DisabledBy   *string     `json:"disabled_by"`
	HiddenBy     *string     `json:"hidden_by"`
}

type haDeviceReg struct {
	ID     string  `json:"id"`
	AreaID *string `json:"area_id"`
}

type entityState struct {
	EntityID   string                 `json:"entity_id"`
	State      string                 `json:"state"`
	Attributes map[string]interface{} `json:"attributes"`
}

// --- Persistent WebSocket connection ---

type haConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
	seq  int

	// Cached registries (loaded on connect)
	areas    []haAreaEntry
	floors   []haFloorEntry
	entities []haEntityReg
	devices  map[string]haDeviceReg
	states   map[string]entityState
}

var haWS haConn

func (h *haConn) disconnect() {
	if h.conn != nil {
		h.conn.Close()
		h.conn = nil
	}
}

// HAClose closes the persistent HA WebSocket connection.
// Can be called from main after query execution.
func HAClose() {
	haWS.mu.Lock()
	defer haWS.mu.Unlock()
	haWS.disconnect()
}

func (h *haConn) ensureConnected() error {
	if h.conn != nil {
		return nil
	}

	cfg, err := getHAConfig()
	if err != nil {
		return err
	}

	wsURL := cfg.URL
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += "/api/websocket"

	ws, err := websocket.Dial(wsURL, "", cfg.URL)
	if err != nil {
		return fmt.Errorf("WS connect: %w", err)
	}
	ws.MaxPayloadBytes = 16 << 20 // 16 MB for large get_states

	// Read auth_required
	var greeting map[string]interface{}
	ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	if err := websocket.JSON.Receive(ws, &greeting); err != nil {
		ws.Close()
		return fmt.Errorf("WS greeting: %w", err)
	}

	// Authenticate
	ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if err := websocket.JSON.Send(ws, map[string]string{
		"type": "auth", "access_token": cfg.Token,
	}); err != nil {
		ws.Close()
		return fmt.Errorf("WS send auth: %w", err)
	}

	var authResp map[string]interface{}
	ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	if err := websocket.JSON.Receive(ws, &authResp); err != nil {
		ws.Close()
		return fmt.Errorf("WS auth response: %w", err)
	}
	if authResp["type"] != "auth_ok" {
		ws.Close()
		return fmt.Errorf("WS auth failed: %v", authResp["message"])
	}

	h.conn = ws
	h.seq = 0

	if err := h.loadCaches(); err != nil {
		h.disconnect()
		return fmt.Errorf("load caches: %w", err)
	}

	return nil
}

// sendCmd sends a WS command and reads the matching result.
// Must be called under h.mu lock.
func (h *haConn) sendCmd(cmdType string, extra map[string]interface{}) (json.RawMessage, error) {
	h.seq++
	id := h.seq

	cmd := map[string]interface{}{"type": cmdType, "id": id}
	for k, v := range extra {
		cmd[k] = v
	}

	h.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if err := websocket.JSON.Send(h.conn, cmd); err != nil {
		h.disconnect()
		return nil, fmt.Errorf("WS send %s: %w", cmdType, err)
	}

	for {
		h.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		var msg wsMsg
		if err := websocket.JSON.Receive(h.conn, &msg); err != nil {
			h.disconnect()
			return nil, fmt.Errorf("WS recv %s: %w", cmdType, err)
		}
		if msg.ID == id && msg.Type == "result" {
			if msg.Success != nil && !*msg.Success {
				errMsg := cmdType + " failed"
				if msg.Error != nil {
					errMsg = msg.Error.Message
				}
				return nil, fmt.Errorf("%s", errMsg)
			}
			return msg.Result, nil
		}
		// Discard messages with wrong ID (stale subscription events, etc.)
	}
}

func (h *haConn) loadCaches() error {
	cmds := []string{
		"config/area_registry/list",
		"config/floor_registry/list",
		"config/entity_registry/list",
		"config/device_registry/list",
		"get_states",
	}

	results := make([]json.RawMessage, len(cmds))
	for i, cmd := range cmds {
		r, err := h.sendCmd(cmd, nil)
		if err != nil {
			return err
		}
		results[i] = r
	}

	if err := json.Unmarshal(results[0], &h.areas); err != nil {
		return fmt.Errorf("parse areas: %w", err)
	}
	if err := json.Unmarshal(results[1], &h.floors); err != nil {
		return fmt.Errorf("parse floors: %w", err)
	}
	sort.Slice(h.floors, func(i, j int) bool { return h.floors[i].Level < h.floors[j].Level })

	if err := json.Unmarshal(results[2], &h.entities); err != nil {
		return fmt.Errorf("parse entities: %w", err)
	}

	var devices []haDeviceReg
	if err := json.Unmarshal(results[3], &devices); err != nil {
		return fmt.Errorf("parse devices: %w", err)
	}
	h.devices = make(map[string]haDeviceReg, len(devices))
	for _, d := range devices {
		h.devices[d.ID] = d
	}

	var states []entityState
	if err := json.Unmarshal(results[4], &states); err != nil {
		return fmt.Errorf("parse states: %w", err)
	}
	h.states = make(map[string]entityState, len(states))
	for _, s := range states {
		h.states[s.EntityID] = s
	}

	return nil
}

func (h *haConn) refreshStates() error {
	result, err := h.sendCmd("get_states", nil)
	if err != nil {
		return err
	}
	var states []entityState
	if err := json.Unmarshal(result, &states); err != nil {
		return fmt.Errorf("parse states: %w", err)
	}
	h.states = make(map[string]entityState, len(states))
	for _, s := range states {
		h.states[s.EntityID] = s
	}
	return nil
}

// --- Entity resolution helpers (called under lock) ---

func (h *haConn) entityAreaID(e haEntityReg) string {
	if e.AreaID != nil && *e.AreaID != "" {
		return *e.AreaID
	}
	if e.DeviceID != nil {
		if dev, ok := h.devices[*e.DeviceID]; ok && dev.AreaID != nil {
			return *dev.AreaID
		}
	}
	return ""
}

func (h *haConn) entityName(e haEntityReg) string {
	if s, ok := h.states[e.EntityID]; ok {
		if fn, _ := s.Attributes["friendly_name"].(string); fn != "" {
			return fn
		}
	}
	if e.Name != nil && *e.Name != "" {
		return *e.Name
	}
	if s, ok := e.OriginalName.(string); ok && s != "" {
		return s
	}
	if e.OriginalName != nil {
		return fmt.Sprintf("%v", e.OriginalName)
	}
	return e.EntityID
}

// --- Formatting ---

func formatAreaList(areas []haAreaEntry, floors []haFloorEntry) string {
	grouped := map[string][]haAreaEntry{}
	var noFloor []haAreaEntry
	for _, a := range areas {
		if a.FloorID != nil && *a.FloorID != "" {
			grouped[*a.FloorID] = append(grouped[*a.FloorID], a)
		} else {
			noFloor = append(noFloor, a)
		}
	}

	var b strings.Builder
	for _, f := range floors {
		aa := grouped[f.FloorID]
		if len(aa) == 0 {
			continue
		}
		b.WriteString(f.Name)
		b.WriteString(":\n")
		for _, a := range aa {
			writeAreaLine(&b, a)
		}
	}
	for _, a := range noFloor {
		writeAreaLine(&b, a)
	}
	return strings.TrimSpace(b.String())
}

func writeAreaLine(b *strings.Builder, a haAreaEntry) {
	b.WriteString("  ")
	b.WriteString(a.Name)
	b.WriteString(" (")
	b.WriteString(a.AreaID)
	b.WriteString(")")
	var aliases []string
	for _, al := range a.Aliases {
		al = strings.TrimSpace(al)
		if al != "" {
			aliases = append(aliases, al)
		}
	}
	if len(aliases) > 0 {
		b.WriteString(" [aka: ")
		b.WriteString(strings.Join(aliases, ", "))
		b.WriteString("]")
	}
	b.WriteString("\n")
}

func formatEntityState(es entityState) string {
	var b strings.Builder
	name, _ := es.Attributes["friendly_name"].(string)
	if name != "" {
		b.WriteString(name)
		b.WriteString(": ")
	}
	b.WriteString(es.State)

	domain := strings.SplitN(es.EntityID, ".", 2)[0]
	var attrs []string

	switch domain {
	case "light":
		if v, ok := es.Attributes["brightness"].(float64); ok {
			attrs = append(attrs, fmt.Sprintf("brightness=%d%%", int(v*100/255)))
		}
		if v, ok := es.Attributes["color_temp_kelvin"].(float64); ok {
			attrs = append(attrs, fmt.Sprintf("color_temp=%dK", int(v)))
		} else if v, ok := es.Attributes["color_temp"].(float64); ok {
			attrs = append(attrs, fmt.Sprintf("color_temp=%d mired", int(v)))
		}
		if v, ok := es.Attributes["rgb_color"]; ok {
			attrs = append(attrs, fmt.Sprintf("rgb=%v", v))
		}
	case "climate":
		if v, ok := es.Attributes["current_temperature"].(float64); ok {
			attrs = append(attrs, fmt.Sprintf("current=%.1f°", v))
		}
		if v, ok := es.Attributes["temperature"].(float64); ok {
			attrs = append(attrs, fmt.Sprintf("target=%.1f°", v))
		}
		if v, ok := es.Attributes["hvac_action"].(string); ok {
			attrs = append(attrs, "action="+v)
		}
	case "cover":
		if v, ok := es.Attributes["current_position"].(float64); ok {
			attrs = append(attrs, fmt.Sprintf("position=%d%%", int(v)))
		}
	case "sensor":
		if v, ok := es.Attributes["unit_of_measurement"].(string); ok {
			attrs = append(attrs, "unit="+v)
		}
		if v, ok := es.Attributes["device_class"].(string); ok {
			attrs = append(attrs, "class="+v)
		}
	case "lock":
		// state itself ("locked"/"unlocked") is sufficient
	}

	if len(attrs) > 0 {
		b.WriteString(" (")
		b.WriteString(strings.Join(attrs, ", "))
		b.WriteString(")")
	}

	return b.String()
}

// --- Tool executors ---

func execHAList(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Target string `json:"target"`
		Domain string `json:"domain"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Target == "" {
		return "", fmt.Errorf("target is required")
	}

	haWS.mu.Lock()
	defer haWS.mu.Unlock()
	if err := haWS.ensureConnected(); err != nil {
		return "", err
	}

	if args.Target == "areas" {
		result := formatAreaList(haWS.areas, haWS.floors)
		if result == "" {
			return "No areas found.", nil
		}
		return result, nil
	}

	// List entities in area
	var b strings.Builder
	for _, e := range haWS.entities {
		if e.DisabledBy != nil || e.HiddenBy != nil {
			continue
		}
		if haWS.entityAreaID(e) != args.Target {
			continue
		}
		domain := strings.SplitN(e.EntityID, ".", 2)[0]
		if args.Domain != "" && domain != args.Domain {
			continue
		}

		state := "unknown"
		if s, ok := haWS.states[e.EntityID]; ok {
			state = s.State
		}
		name := haWS.entityName(e)

		b.WriteString(e.EntityID)
		b.WriteString(" ")
		b.WriteString(state)
		b.WriteString(" (")
		b.WriteString(name)
		b.WriteString(")")
		var aliases []string
		for _, al := range e.Aliases {
			al = strings.TrimSpace(al)
			if al != "" {
				aliases = append(aliases, al)
			}
		}
		if len(aliases) > 0 {
			b.WriteString(" [aka: ")
			b.WriteString(strings.Join(aliases, ", "))
			b.WriteString("]")
		}
		b.WriteString("\n")
	}

	result := strings.TrimSpace(b.String())
	if result == "" {
		msg := fmt.Sprintf("No entities found in area %q", args.Target)
		if args.Domain != "" {
			msg += fmt.Sprintf(" with domain %q", args.Domain)
		}
		return msg + ".", nil
	}
	return result, nil
}

func execHAState(rawArgs json.RawMessage) (string, error) {
	var args struct {
		EntityID string `json:"entity_id"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.EntityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}

	haWS.mu.Lock()
	defer haWS.mu.Unlock()
	if err := haWS.ensureConnected(); err != nil {
		return "", err
	}

	es, ok := haWS.states[args.EntityID]
	if !ok {
		return "", fmt.Errorf("entity %s not found", args.EntityID)
	}
	return formatEntityState(es), nil
}

func execHACall(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Domain   string `json:"domain"`
		Service  string `json:"service"`
		EntityID string `json:"entity_id"`
		Data     string `json:"data"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Domain == "" || args.Service == "" || args.EntityID == "" {
		return "", fmt.Errorf("domain, service, and entity_id are required")
	}

	haWS.mu.Lock()
	defer haWS.mu.Unlock()
	if err := haWS.ensureConnected(); err != nil {
		return "", err
	}

	serviceData := map[string]interface{}{}
	if args.Data != "" {
		if err := json.Unmarshal([]byte(args.Data), &serviceData); err != nil {
			return "", fmt.Errorf("invalid data JSON: %w", err)
		}
	}

	_, err := haWS.sendCmd("call_service", map[string]interface{}{
		"domain":       args.Domain,
		"service":      args.Service,
		"target":       map[string]string{"entity_id": args.EntityID},
		"service_data": serviceData,
	})
	if err != nil {
		return "", fmt.Errorf("call %s.%s: %w", args.Domain, args.Service, err)
	}

	// Wait for state to settle, then refresh cache
	time.Sleep(500 * time.Millisecond)
	if err := haWS.refreshStates(); err != nil {
		return "Service called successfully, but failed to read new state.", nil
	}

	es, ok := haWS.states[args.EntityID]
	if !ok {
		return "Service called successfully.", nil
	}
	return formatEntityState(es), nil
}

// --- Tool registration ---

func init() {
	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "ha_list",
				Description: `Discover Home Assistant areas and entities. Use target="areas" to list all areas grouped by floor (includes aliases). Use target="<area_id>" to list entities in a specific area. Optionally filter by domain (light, cover, climate, sensor, switch, lock, etc).`,
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"target": {
							Type:        "string",
							Description: `"areas" to list all areas, or an area_id (e.g. "living_room") to list entities in that area`,
						},
						"domain": {
							Type:        "string",
							Description: `Filter entities by domain: light, cover, climate, sensor, switch, lock, etc. Only used when target is an area_id.`,
						},
					},
					Required: []string{"target"},
				},
			},
		},
		Execute: execHAList,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "ha_state",
				Description: "Get detailed state of a Home Assistant entity including domain-specific attributes (brightness, temperature, position, etc).",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"entity_id": {
							Type:        "string",
							Description: "The entity ID, e.g. light.living_room_ceiling, climate.bedroom",
						},
					},
					Required: []string{"entity_id"},
				},
			},
		},
		Execute: execHAState,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "ha_call",
				Description: "Call a Home Assistant service to control a device. Common services: light/turn_on, light/turn_off, cover/open_cover, cover/close_cover, cover/set_cover_position, climate/set_temperature, lock/lock, lock/unlock, switch/turn_on, switch/turn_off.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"domain": {
							Type:        "string",
							Description: "Service domain: light, cover, climate, lock, switch, etc.",
						},
						"service": {
							Type:        "string",
							Description: "Service name: turn_on, turn_off, set_temperature, open_cover, etc.",
						},
						"entity_id": {
							Type:        "string",
							Description: "Target entity ID, e.g. light.living_room_ceiling",
						},
						"data": {
							Type:        "string",
							Description: `Optional JSON string with additional service data, e.g. {"brightness": 128} or {"temperature": 22}`,
						},
					},
					Required: []string{"domain", "service", "entity_id"},
				},
			},
		},
		Execute: execHACall,
	})
}
