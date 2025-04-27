// Copyright (C) 2024 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"syscall"

	ffs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
)

type FuseVirtualFolderRoot struct {
	st_folder SyncthingVirtualFolderAccessI
}

type VirtualFolderMount struct {
	fuseServer *fuse.Server
	baseStat   os.FileInfo
}

func (m *VirtualFolderMount) Close() error {
	err := m.fuseServer.Unmount()
	if err != nil {
		return err
	}
	m.fuseServer.Wait()
	return nil
}

type VirtualNode struct {
	ffs.Inode

	RootData *FuseVirtualFolderRoot
	isDir    bool
}

var _ = (ffs.NodeStatfser)((*VirtualNode)(nil))

func (n *VirtualNode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	out.Bavail = 1 << 63
	out.Bfree = out.Bavail
	out.Blocks = 1 << 63
	out.Bsize = 512
	out.Ffree = 1 << 63
	out.Files = 0
	out.NameLen = 1 << 16
	out.Frsize = out.Bsize

	out.Blocks = 202558070
	out.Bfree = 78530783
	out.Bavail = 68223118
	out.Files = 51519488
	out.Ffree = 49368629
	out.Bsize = 4096
	out.NameLen = 255
	out.Frsize = 4096
	out.Padding = 0

	log.Printf("STATFS-LOG: %+v", out)

	return 0
}

// fullPath returns the full path to the file in the underlying file
// system.
func (n *VirtualNode) fullPath() string {
	relative_path := n.Path(n.Root())
	return filepath.Join("", relative_path)
}

func dbInfoToFuseEntryOut(
	info *protocol.FileInfo, ino uint64, name string, out *fuse.EntryOut, ctx context.Context,
) {
	st := syscall.Stat_t{}
	st.Mode = syscall.S_IFREG

	if info != nil {
		st.Size = info.Size
		switch info.Type {
		case protocol.FileInfoTypeDirectory:
			st.Mode = syscall.S_IFDIR
		case protocol.FileInfoTypeSymlink:
			st.Mode = syscall.S_IFLNK
		case protocol.FileInfoTypeFile:
			fallthrough
		default:
		}
	} else {
		st.Size = 1
		st.Mode = syscall.S_IFDIR
	}

	out.Attr.FromStat(&st)
	out.Ino = ino
}

var _ = (ffs.NodeLookuper)((*VirtualNode)(nil))

func (n *VirtualNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*ffs.Inode, syscall.Errno) {

	var eno syscall.Errno = 0
	var info *protocol.FileInfo = nil
	p := filepath.Join(n.fullPath(), name)
	if p != "" {
		info, eno = n.RootData.st_folder.lookupFile(p)
		if eno != ffs.OK {
			return nil, eno
		}
	}

	dbInfoToFuseEntryOut(info, n.RootData.st_folder.getInoOf(p), name, out, ctx)

	child := &VirtualNode{
		RootData: n.RootData,
		isDir:    out.IsDir(),
	}

	return n.NewInode(ctx, child, ffs.StableAttr{
		Mode: out.Mode,
		Ino:  out.Ino,
		Gen:  1,
	}), 0
}

// preserveOwner sets uid and gid of `path` according to the caller information
// in `ctx`.
func (n *VirtualNode) preserveOwner(ctx context.Context, path string) error {
	if os.Getuid() != 0 {
		return nil
	}
	caller, ok := fuse.FromContext(ctx)
	if !ok {
		return nil
	}
	return syscall.Lchown(path, int(caller.Uid), int(caller.Gid))
}

var _ = (ffs.NodeMknoder)((*VirtualNode)(nil))

func (n *VirtualNode) Mknod(ctx context.Context, name string, mode, rdev uint32, out *fuse.EntryOut) (*ffs.Inode, syscall.Errno) {
	//p := filepath.Join(n.fullPath(), name)
	//err := syscall.Mknod(p, mode, int(rdev))
	//if err != nil {
	//	return nil, ffs.ToErrno(err)
	//}
	//n.preserveOwner(ctx, p)
	//st := syscall.Stat_t{}
	//if err := syscall.Lstat(p, &st); err != nil {
	//	syscall.Rmdir(p)
	//	return nil, ffs.ToErrno(err)
	//}
	//
	//out.Attr.FromStat(&st)
	//
	//node := n.RootData.newNode(n.EmbeddedInode(), name, &st)
	//ch := n.NewInode(ctx, node, n.RootData.idFromStat(&st))

	//return ch, 0
	return nil, syscall.ENOSYS
}

var _ = (ffs.NodeMkdirer)((*VirtualNode)(nil))

func (n *VirtualNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*ffs.Inode, syscall.Errno) {
	p := filepath.Join(n.fullPath(), name)
	eno := n.RootData.st_folder.createDir(ctx, p)
	if eno != 0 {
		return nil, eno
	}
	return n.Lookup(ctx, name, out)
}

var _ = (ffs.NodeRmdirer)((*VirtualNode)(nil))

func (n *VirtualNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	p := filepath.Join(n.fullPath(), name)
	return n.RootData.st_folder.deleteDir(ctx, p)
}

var _ = (ffs.NodeUnlinker)((*VirtualNode)(nil))

func (n *VirtualNode) Unlink(ctx context.Context, name string) syscall.Errno {
	p := filepath.Join(n.fullPath(), name)
	return n.RootData.st_folder.deleteFile(ctx, p)
}

var _ = (ffs.NodeRenamer)((*VirtualNode)(nil))

func (n *VirtualNode) Rename(ctx context.Context, name string, newParent ffs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	doRenameExchange := flags&ffs.RENAME_EXCHANGE != 0
	p1 := filepath.Join(n.fullPath(), name)
	p2 := filepath.Join(newParent.EmbeddedInode().Path(nil), newName)

	var eno syscall.Errno
	if doRenameExchange {
		eno = n.RootData.st_folder.renameExchangeFileOrDir(ctx, p1, p2)
	} else {
		eno = n.RootData.st_folder.renameFileOrDir(ctx, p1, p2)
	}

	return eno
}

var _ = (ffs.NodeCreater)((*VirtualNode)(nil))

func (n *VirtualNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut,
) (inode *ffs.Inode, fh ffs.FileHandle, fuseFlags uint32, errno syscall.Errno) {

	logger.DefaultLogger.Infof("VirtualNode Create(parent, file, flags, mode): %s, %v", n.fullPath(), name, flags, mode)

	if !n.isDir {
		return nil, 0, 0, syscall.ENOTDIR
	}

	abs_path := filepath.Join(n.fullPath(), name)

	db_fi, eno := n.RootData.st_folder.createFile(nil, abs_path)
	if eno != 0 {
		return nil, 0, 0, eno
	}

	ino := n.RootData.st_folder.getInoOf(abs_path)
	dbInfoToFuseEntryOut(db_fi, ino, name, out, ctx)

	childHandle := NewVirtualFile(abs_path, ino, n.RootData.st_folder)

	childNode := &VirtualNode{
		RootData: n.RootData,
		isDir:    false,
	}

	ch := n.NewInode(ctx, childNode, ffs.StableAttr{
		Mode: out.Mode,
		Ino:  ino,
		Gen:  1,
	})

	return ch, childHandle, 0, 0
}

var _ = (ffs.NodeSymlinker)((*VirtualNode)(nil))

func (n *VirtualNode) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*ffs.Inode, syscall.Errno) {
	p := filepath.Join(n.fullPath(), name)
	eno := n.RootData.st_folder.createSymlink(ctx, p, target)
	if eno != 0 {
		return nil, eno
	}
	return n.Lookup(ctx, name, out)
}

var _ = (ffs.NodeLinker)((*VirtualNode)(nil))

func (n *VirtualNode) Link(ctx context.Context, target ffs.InodeEmbedder, name string, out *fuse.EntryOut) (*ffs.Inode, syscall.Errno) {

	//p := filepath.Join(n.fullPath(), name)
	//err := syscall.Link(filepath.Join(n.RootData.Path, target.EmbeddedInode().Path(nil)), p)
	//if err != nil {
	//	return nil, ffs.ToErrno(err)
	//}
	//st := syscall.Stat_t{}
	//if err := syscall.Lstat(p, &st); err != nil {
	//	syscall.Unlink(p)
	//	return nil, ffs.ToErrno(err)
	//}
	//node := n.RootData.newNode(n.EmbeddedInode(), name, &st)
	//ch := n.NewInode(ctx, node, n.RootData.idFromStat(&st))
	//
	//out.Attr.FromStat(&st)
	//return ch, 0

	return nil, syscall.ENOSYS
}

var _ = (ffs.NodeReadlinker)((*VirtualNode)(nil))

func (n *VirtualNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	p := n.fullPath()
	info, eno := n.RootData.st_folder.lookupFile(p)
	if eno != ffs.OK {
		return nil, eno
	}

	if info.Type != protocol.FileInfoTypeSymlink {
		return nil, syscall.ENOLINK
	}

	return []byte(info.SymlinkTarget), 0
}

var _ = (ffs.NodeOpener)((*VirtualNode)(nil))

func (n *VirtualNode) Open(ctx context.Context, flags uint32) (fh ffs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	//flags = flags &^ syscall.O_APPEND
	//p := n.fullPath()
	//f, err := syscall.Open(p, int(flags), 0)
	//if err != nil {
	//	return nil, 0, ffs.ToErrno(err)
	//}
	//relative_path := n.Path(n.Root())
	//lf := NewLoopbackFile(relative_path, f, n.RootData.changeChan)
	//return lf, 0, 0

	if n.isDir {
		return nil, 0, syscall.EISDIR
	}

	path := n.fullPath()
	logger.DefaultLogger.Infof("VirtualNode Open(file, flags): %s, %v", path, flags)

	return NewVirtualFile(path, n.RootData.st_folder.getInoOf(path), n.RootData.st_folder), 0, ffs.OK
}

var _ = (ffs.NodeOpendirer)((*VirtualNode)(nil))

func (n *VirtualNode) Opendir(ctx context.Context) syscall.Errno {
	if n.isDir {
		return ffs.OK
	} else {
		return syscall.ENOENT
	}
}

var _ = (ffs.NodeReaddirer)((*VirtualNode)(nil))

func (n *VirtualNode) Readdir(ctx context.Context) (ffs.DirStream, syscall.Errno) {
	return n.RootData.st_folder.readDir(n.Path(n.Root()))
}

var _ = (ffs.NodeGetattrer)((*VirtualNode)(nil))

func (n *VirtualNode) Getattr(ctx context.Context, f ffs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if f != nil {
		ga, ok := f.(ffs.FileGetattrer)
		if ok {
			return ga.Getattr(ctx, out)
		}
	}

	//p := n.fullPath()
	//
	//var err error
	//st := syscall.Stat_t{}
	//if &n.Inode == n.Root() {
	//	err = syscall.Stat(p, &st)
	//} else {
	//	err = syscall.Lstat(p, &st)
	//}^
	//
	//if err != nil {
	//	return ffs.ToErrno(err)
	//}
	//out.FromStat(&st)
	//return ffs.OK

	p := n.fullPath()
	if p != "" {
		info, eno := n.RootData.st_folder.lookupFile(p)
		if eno != ffs.OK {
			return eno
		}

		FileInfoToFuseAttrOut(info, n.RootData.st_folder.getInoOf(p), out)

		out.Size = uint64(info.Size)
		n.isDir = info.Type == protocol.FileInfoTypeDirectory
	}

	if n.isDir {
		out.Mode = syscall.S_IFDIR | 0777
	} else {
		out.Mode = syscall.S_IFREG | 0666
	}

	return ffs.OK
}

var _ = (ffs.NodeSetattrer)((*VirtualNode)(nil))

func (n *VirtualNode) Setattr(ctx context.Context, f ffs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	//p := n.fullPath()
	fsa, ok := f.(ffs.FileSetattrer)
	if ok && fsa != nil {
		fsa.Setattr(ctx, in, out)
	} else {
		logger.DefaultLogger.Infof("VirtualNode Setattr(in,out): %+v, %+v", in, out)
	}

	eno := syscall.EACCES
	fga, ok := f.(ffs.FileGetattrer)
	if ok && fga != nil {
		eno = fga.Getattr(ctx, out)
	} else {
		eno = n.Getattr(ctx, f, out)
	}

	return eno
}

var _ = (ffs.NodeGetxattrer)((*VirtualNode)(nil))

func (n *VirtualNode) Getxattr(ctx context.Context, attr string, dest []byte) (uint32, syscall.Errno) {
	//sz, err := unix.Lgetxattr(n.fullPath(), attr, dest)
	//return uint32(sz), ffs.ToErrno(err)
	return 0, syscall.ENOSYS
}

var _ = (ffs.NodeSetxattrer)((*VirtualNode)(nil))

func (n *VirtualNode) Setxattr(ctx context.Context, attr string, data []byte, flags uint32) syscall.Errno {
	//err := unix.Lsetxattr(n.fullPath(), attr, data, int(flags))
	//return ffs.ToErrno(err)
	return syscall.ENOSYS
}

var _ = (ffs.NodeRemovexattrer)((*VirtualNode)(nil))

func (n *VirtualNode) Removexattr(ctx context.Context, attr string) syscall.Errno {
	//err := unix.Lremovexattr(n.fullPath(), attr)
	//return ffs.ToErrno(err)
	return syscall.ENOSYS
}

var _ = (ffs.NodeCopyFileRanger)((*VirtualNode)(nil))

func (n *VirtualNode) CopyFileRange(ctx context.Context, fhIn ffs.FileHandle,
	offIn uint64, out *ffs.Inode, fhOut ffs.FileHandle, offOut uint64,
	len uint64, flags uint64,
) (uint32, syscall.Errno) {
	//lfIn, ok := fhIn.(*loopbackFile)
	//if !ok {
	//	return 0, unix.ENOTSUP
	//}
	//lfOut, ok := fhOut.(*loopbackFile)
	//if !ok {
	//	return 0, unix.ENOTSUP
	//}
	//signedOffIn := int64(offIn)
	//signedOffOut := int64(offOut)
	//willBeChangedFd(lfOut.fd)
	//doCopyFileRange(lfIn.fd, signedOffIn, lfOut.fd, signedOffOut, int(len), int(flags))
	//return 0, syscall.ENOSYS
	return 0, syscall.ENOSYS
}

func NewVirtualFolderMount(mountPath string, folderId, folderLabel string, stFolder SyncthingVirtualFolderAccessI) (io.Closer, error) {

	root := &FuseVirtualFolderRoot{
		st_folder: stFolder,
	}

	finfo, err := os.Stat(mountPath)
	if err != nil {
		return nil, err
	}

	rootNode := &VirtualNode{
		RootData: root,
		isDir:    true,
	}

	// convenience: umount if still uncleanly mounted from previous run
	_ = syscall.Unmount(mountPath, 0)

	fuseServer, err := ffs.Mount(mountPath, rootNode, &ffs.Options{
		MountOptions: fuse.MountOptions{
			FsName: "syncthing/" + folderId,
			Name:   "syncthing",
		},
		FirstAutomaticIno: 2,
		Logger:            log.Default(),
	})

	log.Printf("MOUNTED folderId %s to path %s, err: %v", folderId, mountPath, err)

	if err != nil {
		return nil, err
	}

	fuseServer.SetDebug(true)

	mount := &VirtualFolderMount{
		fuseServer: fuseServer,
		baseStat:   finfo,
	}

	return mount, nil
}
