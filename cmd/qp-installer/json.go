package main

import enc "encoding/json"

func json(b []byte, v any) error { return enc.Unmarshal(b, v) }
