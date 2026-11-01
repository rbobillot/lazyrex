package gui

import "testing"

func TestArrange(t *testing.T) {
	sides := []Panel{PanelTargets, PanelSessions, PanelWindows, PanelBlocks, PanelClients}
	for _, size := range [][2]int{{120, 40}, {60, 20}, {200, 60}} {
		w, hgt := size[0], size[1]
		for _, focus := range append(sides, PanelMain) {
			rects := Arrange(w, hgt, focus)
			for _, name := range []string{"targets", "sessions", "windows", "blocks", "clients", "main", "log", "footer"} {
				r, ok := rects[name]
				if !ok {
					t.Fatalf("%dx%d: no rect for %s", w, hgt, name)
				}
				if r.X0 < 0 || r.Y0 < 0 || r.X1 > w-1 || r.Y1 > hgt-1 || r.X1 < r.X0 || r.Y1 < r.Y0 {
					t.Errorf("%dx%d focus %v: %s out of bounds: %+v", w, hgt, focus, name, r)
				}
			}
			names := []string{}
			for n := range rects {
				names = append(names, n)
			}
			for i := 0; i < len(names); i++ {
				for j := i + 1; j < len(names); j++ {
					a, b := rects[names[i]], rects[names[j]]
					if a.X0 <= b.X1 && b.X0 <= a.X1 && a.Y0 <= b.Y1 && b.Y0 <= a.Y1 {
						t.Errorf("%dx%d focus %v: %s %+v overlaps %s %+v", w, hgt, focus, names[i], a, names[j], b)
					}
				}
			}
			if focus != PanelMain {
				r := rects[viewName(focus)]
				if r.Y1-r.Y0-1 < 3 {
					t.Errorf("%dx%d: focused %v has %d content rows", w, hgt, focus, r.Y1-r.Y0-1)
				}
			}
			for _, p := range sides {
				if r := rects[viewName(p)]; r.Y1-r.Y0-1 < 1 {
					t.Errorf("%dx%d focus %v: %v has no content row", w, hgt, focus, p)
				}
			}
		}
	}
}
