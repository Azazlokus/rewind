package game

import "arena/internal/protocol"

// Movement constants. The client mirrors these values in web/game.js; changing
// one side only shows up as prediction drift, not as a compile error.
const (
	// PlayerSpeed is the movement speed in world units per second.
	PlayerSpeed float32 = 300
	// PlayerRadius is the collision radius of a player in world units.
	PlayerRadius float32 = 16
	// MapSize is the side of the square map in world units.
	MapSize float32 = protocol.MapSize
	// invSqrt2 normalises diagonal movement.
	invSqrt2 float32 = 0.70710678
)

// MoveState is the part of an entity that the shared movement step touches.
type MoveState struct {
	X, Y   float32
	VX, VY float32
}

// Step advances one entity by dt seconds under input in.
//
// This is the single definition of player movement. The client runs the same
// function for prediction (iteration 4), so the constants, the order of the
// operations and the float32 rounding have to stay identical on both sides.
func Step(s *MoveState, in protocol.Input, dt float32) {
	var dx, dy float32
	if in.Buttons&protocol.BtnLeft != 0 {
		dx -= 1
	}
	if in.Buttons&protocol.BtnRight != 0 {
		dx += 1
	}
	if in.Buttons&protocol.BtnUp != 0 {
		dy -= 1
	}
	if in.Buttons&protocol.BtnDown != 0 {
		dy += 1
	}
	if dx != 0 && dy != 0 {
		dx *= invSqrt2
		dy *= invSqrt2
	}
	s.VX = dx * PlayerSpeed
	s.VY = dy * PlayerSpeed
	s.X = clamp(s.X+s.VX*dt, PlayerRadius, MapSize-PlayerRadius)
	s.Y = clamp(s.Y+s.VY*dt, PlayerRadius, MapSize-PlayerRadius)
}

func clamp(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
