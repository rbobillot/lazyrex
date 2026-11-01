package rex

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The containment readers decode `session inspect` and `block ls` strictly,
// for the own-block check before a destroy: a field of section 8's shapes
// that is missing or null, a null entry or an ID that is not full makes
// containment unknown, never "no blocks". Only an explicit [] is empty.
// In the windows checked, a layer's layout must be an object whose block_id
// and block_ids name only full IDs, which count as contained too; Rex sends
// a null layout for a layer with no blocks only.

type strictBlock struct {
	BlockID *BlockID `json:"block_id"`
}

type strictLayer struct {
	Blocks *[]*strictBlock `json:"blocks"`
	Layout json.RawMessage `json:"layout"`
}

type strictWindow struct {
	WindowID *WindowID       `json:"window_id"`
	Layers   *[]*strictLayer `json:"layers"`
}

type strictView struct {
	SessionID *SessionID       `json:"session_id"`
	Windows   *[]*strictWindow `json:"windows"`
}

func blockIDs(where string, list *[]*strictBlock) ([]BlockID, error) {
	if list == nil {
		return nil, fmt.Errorf("%s has no blocks", where)
	}
	ids := make([]BlockID, 0, len(*list))
	for i, b := range *list {
		if b == nil || b.BlockID == nil || !b.BlockID.Valid() {
			return nil, fmt.Errorf("%s: block %d has no full block_id", where, i)
		}
		ids = append(ids, *b.BlockID)
	}
	return ids, nil
}

// layoutBlocks returns the IDs that block_id and block_ids name anywhere in
// a layout tree, which may be missing or null only when the layer is empty.
func layoutBlocks(raw json.RawMessage, empty bool) ([]BlockID, error) {
	var tree any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &tree); err != nil {
			return nil, err
		}
	}
	switch tree.(type) {
	case nil:
		if empty {
			return nil, nil
		}
		return nil, errors.New("no layout for a layer with blocks")
	case map[string]any:
	default:
		return nil, errors.New("layout is not an object")
	}
	var ids []BlockID
	add := func(key string, v any) error {
		s, ok := v.(string)
		if !ok || !BlockID(s).Valid() {
			return fmt.Errorf("layout %s is not a full block ID", key)
		}
		ids = append(ids, BlockID(s))
		return nil
	}
	var walk func(any) error
	walk = func(n any) error {
		switch n := n.(type) {
		case map[string]any:
			for k, v := range n {
				switch k {
				case "block_id":
					if v != nil {
						if err := add(k, v); err != nil {
							return err
						}
					}
				case "block_ids":
					list, ok := v.([]any)
					if !ok {
						return errors.New("layout block_ids is not an array")
					}
					for _, e := range list {
						if err := add(k, e); err != nil {
							return err
						}
					}
				}
				if err := walk(v); err != nil {
					return err
				}
			}
		case []any:
			for _, v := range n {
				if err := walk(v); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(tree); err != nil {
		return nil, err
	}
	return ids, nil
}

// ContainedBlocks reads a `session inspect` of session s and returns the
// blocks of its window w, or of every window when w is empty. Every window
// is checked whole, since a window with no ID could be w.
func ContainedBlocks(b []byte, s SessionID, w WindowID) ([]BlockID, error) {
	var v strictView
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("parse session inspect: %w", err)
	}
	switch {
	case v.SessionID == nil || !v.SessionID.Valid():
		return nil, errors.New("session inspect has no full session_id")
	case !strings.EqualFold(string(*v.SessionID), string(s)):
		return nil, fmt.Errorf("session inspect describes %s, not %s", *v.SessionID, s)
	case v.Windows == nil:
		return nil, errors.New("session inspect has no windows")
	}
	var ids []BlockID
	found := w == ""
	for i, win := range *v.Windows {
		if win == nil || win.WindowID == nil || !win.WindowID.Valid() {
			return nil, fmt.Errorf("session inspect: window %d has no full window_id", i)
		}
		if win.Layers == nil {
			return nil, fmt.Errorf("session inspect: %s has no layers", *win.WindowID)
		}
		wanted := w == "" || strings.EqualFold(string(*win.WindowID), string(w))
		found = found || wanted
		for j, l := range *win.Layers {
			if l == nil {
				return nil, fmt.Errorf("session inspect: %s layer %d is null", *win.WindowID, j)
			}
			layer, err := blockIDs(fmt.Sprintf("session inspect: %s layer %d", *win.WindowID, j), l.Blocks)
			if err != nil {
				return nil, err
			}
			if wanted {
				named, err := layoutBlocks(l.Layout, len(layer) == 0)
				if err != nil {
					return nil, fmt.Errorf("session inspect: %s layer %d: %w", *win.WindowID, j, err)
				}
				ids = append(append(ids, layer...), named...)
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("session inspect has no %s", w)
	}
	return ids, nil
}

// ListedBlocks reads a `block ls` and returns every block it lists, detached
// ones included.
func ListedBlocks(b []byte) ([]BlockID, error) {
	var v struct {
		Blocks *[]*strictBlock `json:"blocks"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("parse block ls: %w", err)
	}
	return blockIDs("block ls", v.Blocks)
}
