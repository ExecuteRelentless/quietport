package main

import enc "encoding/json"

func json(b []byte, v any) error { return enc.Unmarshal(b, v) }

func jsonMarshal(v any) ([]byte, error) { return enc.Marshal(v) }
