package domain

type Point3 struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
	Z int64 `json:"z"`
}

type Box struct {
	Length int64 `json:"length"`
	Width  int64 `json:"width"`
	Height int64 `json:"height"`
}

func (value Box) Valid() bool {
	return value.Length > 0 && value.Width > 0 && value.Height > 0
}

func (value Box) VolumeMM3() int64 {
	return value.Length * value.Width * value.Height
}

func (value Box) Oriented(orientation Orientation) (Box, bool) {
	switch orientation {
	case OrientationLWH:
		return value, true
	case OrientationLHW:
		return Box{Length: value.Length, Width: value.Height, Height: value.Width}, true
	case OrientationWLH:
		return Box{Length: value.Width, Width: value.Length, Height: value.Height}, true
	case OrientationWHL:
		return Box{Length: value.Width, Width: value.Height, Height: value.Length}, true
	case OrientationHLW:
		return Box{Length: value.Height, Width: value.Length, Height: value.Width}, true
	case OrientationHWL:
		return Box{Length: value.Height, Width: value.Width, Height: value.Length}, true
	default:
		return Box{}, false
	}
}

type Cuboid struct {
	Origin Point3 `json:"origin"`
	Size   Box    `json:"size"`
}

func (value Cuboid) Max() Point3 {
	return Point3{
		X: value.Origin.X + value.Size.Length,
		Y: value.Origin.Y + value.Size.Width,
		Z: value.Origin.Z + value.Size.Height,
	}
}

func (value Cuboid) Contains(other Cuboid) bool {
	valueMax := value.Max()
	otherMax := other.Max()
	return other.Origin.X >= value.Origin.X &&
		other.Origin.Y >= value.Origin.Y &&
		other.Origin.Z >= value.Origin.Z &&
		otherMax.X <= valueMax.X &&
		otherMax.Y <= valueMax.Y &&
		otherMax.Z <= valueMax.Z
}

func (value Cuboid) IntersectsOpen(other Cuboid) bool {
	valueMax := value.Max()
	otherMax := other.Max()
	return value.Origin.X < otherMax.X && other.Origin.X < valueMax.X &&
		value.Origin.Y < otherMax.Y && other.Origin.Y < valueMax.Y &&
		value.Origin.Z < otherMax.Z && other.Origin.Z < valueMax.Z
}

func (value Cuboid) IntersectionXYArea(other Cuboid) int64 {
	valueMax := value.Max()
	otherMax := other.Max()
	length := minInt64(valueMax.X, otherMax.X) - maxInt64(value.Origin.X, other.Origin.X)
	width := minInt64(valueMax.Y, otherMax.Y) - maxInt64(value.Origin.Y, other.Origin.Y)
	if length <= 0 || width <= 0 {
		return 0
	}
	return length * width
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
