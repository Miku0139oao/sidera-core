//go:build generate && generate_completions

package main

import "github.com/Miku0139oao/sidera-core/log"

func main() {
	err := generateCompletions()
	if err != nil {
		log.Fatal(err)
	}
}

func generateCompletions() error {
	err := mainCommand.GenBashCompletionFile("release/completions/sidera.bash")
	if err != nil {
		return err
	}
	err = mainCommand.GenFishCompletionFile("release/completions/sidera.fish", true)
	if err != nil {
		return err
	}
	err = mainCommand.GenZshCompletionFile("release/completions/sidera.zsh")
	if err != nil {
		return err
	}
	return nil
}
