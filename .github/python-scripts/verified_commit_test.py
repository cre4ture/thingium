import base64
import datetime
import json
import os
from pprint import pprint
import sys
import github

import requests

from github import Github


# If you run this example using your personal token the commit is not going to be verified.
# It only works for commits made using a token generated for a bot/app like the one you have
# during the workflow job execution.
# The example workflow "example-01.yml" uses the GITHUB_TOKEN and auto-commits are verified.

def main(repo_token, repo, branch, file_to_update_remote, file_to_update_local):

    gh = Github(repo_token)

    remote_repo = gh.get_repo(repo)

    # Get current file sha

    print ("File to update remote: ", file_to_update_remote)
    parent_dir_remote = os.path.dirname(file_to_update_remote)
    print("Parent dir remote: ", parent_dir_remote)
    dir_content = remote_repo.get_contents(parent_dir_remote, branch)

    remote_sha = None

    for file in dir_content:
        if (file.path == file_to_update_remote):
            print("File: ", file.path, " sha:", file.sha)
            remote_sha = file.sha

    if remote_sha is None:
        raise ValueError(f'File not found {file_to_update_remote}')

    # Update file

    # read local file
    new_content = ""
    with open(file_to_update_local, 'r') as file:
        new_content = file.read()

    commit_message = f'Example 01: update content at {datetime.datetime.now()}'

    response = remote_repo.update_file(
        file_to_update_remote, commit_message, new_content, remote_sha, branch)

    pprint(response)
    print("Commit sha: ", response['commit'].sha)

def main2(repo_token, repo, branch):

    gh = github.Github(repo_token)

    remote_repo = gh.get_repo(repo)

    # Update files:
    #   data/example-04/latest_datetime_01.txt
    #   data/example-04/latest_datetime_02.txt
    # with the current date.

    file_to_update_01 = "data/example-04/latest_datetime_01.txt"
    file_to_update_02 = "data/example-04/latest_datetime_02.txt"

    now = datetime.datetime.now()

    file_to_update_01_content = str(now)
    file_to_update_02_content = str(now)

    blob1 = remote_repo.create_git_blob(file_to_update_01_content, "utf-8")
    element1 = github.InputGitTreeElement(
        path=file_to_update_01, mode='100644', type='blob', sha=blob1.sha)

    blob2 = remote_repo.create_git_blob(file_to_update_02_content, "utf-8")
    element2 = github.InputGitTreeElement(
        path=file_to_update_02, mode='100644', type='blob', sha=blob2.sha)

    commit_message = f'Example 04: update datetime to {now}'

    branch_sha = remote_repo.get_branch(branch).commit.sha

    print("Branch sha: ", branch_sha)

    base_tree = remote_repo.get_git_tree(sha=branch_sha)

    print("Base tree: ", base_tree)

    tree = remote_repo.create_git_tree([element1, element2], base_tree)

    print("Tree: ", tree)

    parent = remote_repo.get_git_commit(sha=branch_sha)

    print("Parent: ", parent)

    commit = remote_repo.create_git_commit(commit_message, tree, [parent])

    print("New commit: ", commit)

    branch_refs = remote_repo.get_git_ref(f'heads/{branch}')

    print("Banch refs: ", branch_refs)

    branch_refs.edit(sha=commit.sha)

    print("New branch ref: ", commit.sha)


def push_to_github(filename, repo, branch, token):
    url="https://api.github.com/repos/"+repo+"/contents/"+filename

    base64content=base64.b64encode(open(filename,"rb").read())

    data = requests.get(url+'?ref='+branch, headers = {"Authorization": "token "+token}).json()
    sha = data['sha']

    changed = base64content.decode('utf-8')+"\n" != data['content']
    if changed:
        message = json.dumps({"message":"update",
                            "branch": branch,
                            "content": base64content.decode("utf-8") ,
                            "sha": sha
                            })

        resp=requests.put(url, data = message, headers = {"Content-Type": "application/json", "Authorization": "token "+token})

        print(resp)
    else:
        print("nothing to update")

if __name__ == "__main__":
    # https://pygithub.readthedocs.io
    repo_token = os.environ["GITHUB_TOKEN_TMP"]
    repo = os.environ["REPOSITORY"]
    # get branch from command line parameter
    branch = sys.argv[1]
    file_to_update_local = sys.argv[2]
    file_to_update_remote = sys.argv[3]
    #main(repo_token, repo, branch, file_to_update_remote, file_to_update_local)
    #push_to_github(file_to_update_local, repo, branch, repo_token)