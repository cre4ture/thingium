import datetime
import os
from pprint import pprint
import sys

from github import Github


# If you run this example using your personal token the commit is not going to be verified.
# It only works for commits made using a token generated for a bot/app like the one you have
# during the workflow job execution.
# The example workflow "example-01.yml" uses the GITHUB_TOKEN and auto-commits are verified.

def main(repo_token, branch, file_to_update_remote, file_to_update_local):

    gh = Github(repo_token)

    repository = "josecelano/pygithub"

    remote_repo = gh.get_repo(repository)

    # Get current file sha

    dir_content = remote_repo.get_contents("data/example-01", branch)

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

    commit_message = f'Example 01: update content to {new_content}'

    response = remote_repo.update_file(
        file_to_update_remote, commit_message, new_content, remote_sha, branch)

    pprint(response)
    print("Commit sha: ", response['commit'].sha)


if __name__ == "__main__":
    # https://pygithub.readthedocs.io
    repo_token = os.environ["INPUT_REPO_TOKEN"]
    # get branch from command line parameter
    branch = sys.argv[1]
    file_to_update_local = sys.argv[2]
    file_to_update_remote = sys.argv[3]
    main(repo_token, branch, file_to_update_remote, file_to_update_local)