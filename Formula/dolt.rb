class Dolt < Formula
  desc "Dolt - It's git for data"
  homepage 'https://github.com/fanahova/dolt'
  url 'https://github.com/fanahova/dolt/archive/v0.9.7.tar.gz'
  version '0.9.7'
  sha256 '931d1ba53a54d8aef08c2f9f27e16160eea059a3dae1c95ba6ca6760ea7be794'

  depends_on 'go' => :build

  bottle :unneeded

  def install
    system 'cd go'
    system 'go install ./cmd/dolt'
    system 'go install ./cmd/git-dolt'
    system 'go install ./cmd/git-dolt-smudge'
  end

  test do
    system "#{bin}/dolt", '--version'
  end
end