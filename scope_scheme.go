package autotag

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"

	"github.com/gogs/git-module"
	"github.com/hashicorp/go-version"
)

var (
	// versionRex matches semVer style versions, eg: `account-v1.0.0`
	// https://regex101.com/r/rhHnSO/1
	scopeVersionRex = regexp.MustCompile(`^(.*)-v?([\d]+\.?.*)`)

	// scope conventional commit message scheme:
	// https://regex101.com/r/XciTmT/2
	scopeConventionalCommitRex = regexp.MustCompile(`^\s*(?P<type>\w+)(?P<scope>(?:\([^()\r\n]*\)|\()?(?P<breaking>!)?)(?P<subject>:.*)?`)
)

type CommitMessage struct {
	ype      string
	scope    string
	breaking string
	subject  string
}

func (r *GitRepo) scopeSchemeCalcVersion() error {
	var (
		latestCommit        *git.Commit
		latestCommitMessage CommitMessage
		err                 error
	)

	// 设置最新提交id branchID
	if err = r.retrieveBranchInfo(); err != nil {
		return err
	}
	// latestCommit: 最新的提交
	latestCommit, err = r.repo.BranchCommit(r.branch)
	if err != nil {
		return err
	}
	// 解析commit message
	matches := findNamedMatches(scopeConventionalCommitRex, latestCommit.Message)
	_, after, _ := strings.Cut(matches["scope"], "(")
	before, _, _ := strings.Cut(after, ")")
	latestCommitMessage = CommitMessage{
		ype:      matches["type"],
		scope:    before,
		breaking: matches["breaking"],
		subject:  matches["subject"],
	}
	// 提交信息不包含Scope，将不设置tag
	if latestCommitMessage.scope == "" {
		return nil
	}
	r.scope = latestCommitMessage.scope

	versions := make(map[*version.Version]*git.Commit)
	tagNames, err := r.repo.Tags()
	if err != nil {
		return fmt.Errorf("failed to fetch tags: %s", err.Error())
	}

	for _, tagName := range tagNames {
		// 过滤出此 scope 版本号
		var (
			tagScope   string
			tagVersion string
			v          *version.Version
			c          *git.Commit
		)
		if scopeVersionRex.MatchString(tagName) {
			m := scopeVersionRex.FindStringSubmatch(tagName)
			if len(m) >= 2 {
				tagScope = m[1]
				tagVersion = m[2]
			}
		} else {
			continue
		}
		if tagScope != latestCommitMessage.scope {
			log.Println("no scope find, skipping new version")
			continue
		}

		v, err = maybeVersionFromTag(tagVersion)
		if err != nil {
			log.Println("skipping non version tag: ", tagName)
			continue
		}
		if v == nil {
			log.Println("skipping non version tag: ", tagName)
			continue
		}

		c, err = r.repo.CommitByRevision(tagName)
		if err != nil {
			return fmt.Errorf("error reading tag '%s':  %s", tagName, err)
		}
		versions[v] = c
	}

	keys := make([]*version.Version, 0, len(versions))
	for key := range versions {
		keys = append(keys, key)
	}
	sort.Sort(sort.Reverse(version.Collection(keys)))
	for _, v := range keys {
		if len(v.Prerelease()) == 0 {
			r.currentVersion = v
			r.currentTag = versions[v]
			break
		}
		log.Printf("skipping pre-release tag version: %s\n", v.String())
	}
	if r.currentVersion == nil || r.currentTag == nil {
		return fmt.Errorf("no stable (non pre-release) version %s tags found", latestCommitMessage.scope)
	}
	log.Printf("currentVersion: %s, currentTagCommit: %s\n", r.currentVersion.String(), r.currentTag.Message)

	// 提交信息包含`Scope`，提交头中包含`!`（`Scope`之后）则被认为是`BREAKING CHANGE:`，将自增`Scope Major`版本
	if latestCommitMessage.breaking == "!" {
		r.newVersion, err = r.MajorBump()
		if err != nil {
			return err
		}
	} else if latestCommitMessage.ype == "feat" {
		r.newVersion, err = r.MinorBump()
		if err != nil {
			return err
		}
	} else {
		r.newVersion, err = r.PatchBump()
		if err != nil {
			return err
		}
	}

	// append pre-release-name and/or pre-release-timestamp to the version
	if len(r.preReleaseName) > 0 || len(r.preReleaseTimestampLayout) > 0 {
		if r.newVersion, err = preReleaseVersion(r.newVersion, r.preReleaseName, r.preReleaseTimestampLayout); err != nil {
			return err
		}
	}

	// append optional build metadata
	if r.buildMetadata != "" {
		if r.newVersion, err = version.NewVersion(fmt.Sprintf("%s+%s", r.newVersion.String(), r.buildMetadata)); err != nil {
			return err
		}
	}

	return nil
}
