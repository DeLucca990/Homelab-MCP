package mcp

import "testing"

func TestFingerprintKeepsPartBoundaries(t *testing.T) {
	if fingerprint("rm", "-rf") == fingerprint("rm-rf") {
		t.Error(`["rm","-rf"] and ["rm-rf"] share a fingerprint`)
	}
	if fingerprint("ab", "x") == fingerprint("a", "bx") {
		t.Error(`["ab","x"] and ["a","bx"] share a fingerprint`)
	}
}

func TestDockerFingerprintsNameTheTool(t *testing.T) {
	restart := restartFingerprint(restartInput{Container: "radarr"})
	execRestart := execFingerprint(execInput{Container: "radarr", Command: []string{"restart"}})
	if restart == execRestart {
		t.Error("approving a restart of radarr would authorise running `restart` inside it")
	}

	if execFingerprint(execInput{Container: "radarr", Command: []string{"ls"}}) ==
		execFingerprint(execInput{Container: "sonarr", Command: []string{"ls"}}) {
		t.Error("the container is not part of the exec fingerprint")
	}
	if restart == restartFingerprint(restartInput{Container: "sonarr"}) {
		t.Error("the container is not part of the restart fingerprint")
	}
}
